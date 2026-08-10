// Package cfapi is the shared transport for every Cloudflare v4 API call
// upkeep makes: R2 buckets, their settings, and Pages projects.
package cfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// String redacts the token so that printing a Client cannot leak it.
func (c *Client) String() string {
	return fmt.Sprintf("cfapi.Client{baseURL: %s, token: [redacted]}", c.baseURL)
}

type envelope struct {
	Success  bool            `json:"success"`
	Errors   []apiMessage    `json:"errors"`
	Messages []apiMessage    `json:"messages"`
	Result   json.RawMessage `json:"result"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error is a Cloudflare API failure with its code kept, so a caller can tell
// "does not exist yet" from "you are not allowed to ask".
type Error struct {
	Status   int
	Code     int
	Message  string
	Endpoint string
}

func (e *Error) Error() string {
	return fmt.Sprintf("cloudflare %s: %d %s (HTTP %d)", e.Endpoint, e.Code, e.Message, e.Status)
}

// NotConfigured reports the codes R2 returns for a setting that has never been
// set. They are not failures — an absent CORS policy is the normal state of a
// new bucket, and treating it as an error would make the first plan unusable.
func NotConfigured(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case 10059, // The CORS configuration does not exist.
		10006, // Bucket not found.
		10042: // Public access is not enabled.
		return true
	}
	return apiErr.Status == http.StatusNotFound
}

// Forbidden reports a permission failure, which upkeep turns into a manual
// action rather than a crash: the operator can do in a dashboard what this
// token may not.
func Forbidden(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusForbidden ||
		apiErr.Status == http.StatusUnauthorized ||
		apiErr.Code == 9109 // Unauthorized to access requested resource.
}

// Do performs one request. body is JSON-marshalled when non-nil; result is
// JSON-unmarshalled from the envelope's result field when non-nil.
func (c *Client) Do(ctx context.Context, method, path string, body, result any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("cloudflare %s: read body: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return &Error{Status: res.StatusCode, Message: strings.TrimSpace(string(payload)), Endpoint: path}
	}
	if !env.Success {
		apiErr := &Error{Status: res.StatusCode, Endpoint: path, Message: "unknown error"}
		if len(env.Errors) > 0 {
			apiErr.Code = env.Errors[0].Code
			apiErr.Message = env.Errors[0].Message
		}
		return apiErr
	}
	if result != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("cloudflare %s: decode result: %w", path, err)
		}
	}
	return nil
}
