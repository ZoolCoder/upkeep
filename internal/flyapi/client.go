// Package flyapi talks to Fly.io's Machines API: an app's secrets, and the
// releases that restart it.
//
// Fly names things differently from Render on purpose, and the difference
// matters to what upkeep can honestly report. Fly calls them *secrets*, and the
// API never returns a value — only a name and a digest. So upkeep can tell you
// that a secret is missing, and that one exists, but not whether its value is
// the one you meant. That limit is the provider's, not a gap here, and every
// plan says so rather than implying a comparison it did not make.
package flyapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		baseURL = "https://api.machines.dev/v1"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// String redacts the token so that printing a Client cannot leak it.
func (c *Client) String() string {
	return fmt.Sprintf("flyapi.Client{baseURL: %s, token: [redacted]}", c.baseURL)
}

type Error struct {
	Status   int
	Message  string
	Endpoint string
}

func (e *Error) Error() string {
	return fmt.Sprintf("fly %s: %s (HTTP %d)", e.Endpoint, e.Message, e.Status)
}

// Secret is what Fly will tell you: a name, a kind, and a digest. Never a
// value.
type Secret struct {
	Name   string `json:"label"`
	Digest string `json:"digest"`
	Type   string `json:"type"`
}

// Secrets lists the names an app has set.
func (c *Client) Secrets(ctx context.Context, app string) (map[string]Secret, error) {
	var list []Secret
	if err := c.do(ctx, http.MethodGet, "/apps/"+app+"/secrets", nil, &list); err != nil {
		return nil, err
	}
	out := make(map[string]Secret, len(list))
	for _, s := range list {
		out[s.Name] = s
	}
	return out, nil
}

// SetSecret sets one secret. Fly stages secrets and applies them on the next
// release, which is why setting one is not the same as it being live.
func (c *Client) SetSecret(ctx context.Context, app, name, value string) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/apps/%s/secrets/%s/type/env", app, name),
		map[string]string{"value": value}, nil)
}

// App is the subset upkeep reads.
type App struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (c *Client) App(ctx context.Context, app string) (App, error) {
	var out App
	err := c.do(ctx, http.MethodGet, "/apps/"+app, nil, &out)
	return out, err
}

// TokenFromCLI reads the session `fly auth login` already stored, so an
// operator who can run flyctl needs no second credential on disk.
func TokenFromCLI() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	paths := []string{filepath.Join(home, ".fly", "config.yml")}
	if appData := os.Getenv("APPDATA"); appData != "" {
		paths = append(paths, filepath.Join(appData, "fly", "config.yml"))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			// access_token: FlyV1 …
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) != "access_token" {
				continue
			}
			if token := strings.Trim(strings.TrimSpace(value), `"'`); token != "" {
				return token
			}
		}
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
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
		return fmt.Errorf("fly %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("fly %s: read body: %w", path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := strings.TrimSpace(string(payload))
		var wrapped struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &wrapped) == nil && wrapped.Error != "" {
			message = wrapped.Error
		}
		return &Error{Status: res.StatusCode, Message: message, Endpoint: path}
	}
	if result != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, result); err != nil {
			return fmt.Errorf("fly %s: decode result: %w", path, err)
		}
	}
	return nil
}
