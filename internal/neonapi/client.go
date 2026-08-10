// Package neonapi reads a Neon project and its branches.
//
// Read-only on purpose. A database is not a thing to converge silently: a plan
// that offers to create or delete one is a plan somebody eventually runs by
// accident.
package neonapi

import (
	"context"
	"encoding/json"
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
		baseURL = "https://console.neon.tech/api/v2"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// String redacts the token so that printing a Client cannot leak it.
func (c *Client) String() string {
	return fmt.Sprintf("neonapi.Client{baseURL: %s, token: [redacted]}", c.baseURL)
}

type Error struct {
	Status   int
	Message  string
	Endpoint string
}

func (e *Error) Error() string {
	return fmt.Sprintf("neon %s: %s (HTTP %d)", e.Endpoint, e.Message, e.Status)
}

type Branch struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// Branches lists a project's branches.
func (c *Client) Branches(ctx context.Context, projectID string) ([]Branch, error) {
	var out struct {
		Branches []Branch `json:"branches"`
	}
	err := c.do(ctx, http.MethodGet, "/projects/"+projectID+"/branches", &out)
	return out.Branches, err
}

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Project(ctx context.Context, projectID string) (Project, error) {
	var out struct {
		Project Project `json:"project"`
	}
	err := c.do(ctx, http.MethodGet, "/projects/"+projectID, &out)
	return out.Project, err
}

func (c *Client) do(ctx context.Context, method, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("neon %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("neon %s: read body: %w", path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := strings.TrimSpace(string(payload))
		var wrapped struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &wrapped) == nil && wrapped.Message != "" {
			message = wrapped.Message
		}
		return &Error{Status: res.StatusCode, Message: message, Endpoint: path}
	}
	if result != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, result); err != nil {
			return fmt.Errorf("neon %s: decode result: %w", path, err)
		}
	}
	return nil
}
