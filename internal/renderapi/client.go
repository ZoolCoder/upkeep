// Package renderapi talks to Render's v1 API: a service's environment, and the
// deploys that restart it.
//
// Render is not enveloped like Cloudflare — a failure is an HTTP status with a
// JSON message — so the two transports stay separate rather than sharing an
// abstraction that fits neither.
package renderapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
		baseURL = "https://api.render.com/v1"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// String redacts the token so that printing a Client cannot leak it.
func (c *Client) String() string {
	return fmt.Sprintf("renderapi.Client{baseURL: %s, token: [redacted]}", c.baseURL)
}

// cliKey pulls the API key out of the render CLI's own config, so an operator
// who has run `render login` needs no second credential on disk. Returns ""
// when the CLI has never been used.
var cliKey = regexp.MustCompile(`(?m)^\s+key:\s*(\S+)`)

// TokenFromCLI reads ~/.render/cli.yaml. The value is returned, never logged.
func TokenFromCLI() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	paths := []string{filepath.Join(home, ".render", "cli.yaml")}
	if appData := os.Getenv("APPDATA"); appData != "" {
		paths = append(paths, filepath.Join(appData, "render", "cli.yaml"))
	}
	var raw []byte
	for _, path := range paths {
		if raw, err = os.ReadFile(path); err == nil {
			break
		}
	}
	if err != nil {
		return ""
	}
	// The key lives under an `api:` block; take the first indented key: line
	// after it rather than any key: anywhere in the file.
	text := string(raw)
	at := strings.Index(text, "\napi:")
	if at < 0 {
		return ""
	}
	m := cliKey.FindStringSubmatch(text[at:])
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

type Error struct {
	Status   int
	Message  string
	Endpoint string
}

func (e *Error) Error() string {
	return fmt.Sprintf("render %s: %s (HTTP %d)", e.Endpoint, e.Message, e.Status)
}

// EnvVar is one variable as Render reports it.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type envVarItem struct {
	EnvVar EnvVar `json:"envVar"`
}

// EnvVars returns the service's whole environment, keyed by name.
//
// The values come back too — Render's API does not redact them — so callers
// must compare and discard rather than print. Nothing in upkeep renders a
// value from this map.
func (c *Client) EnvVars(ctx context.Context, serviceID string) (map[string]string, error) {
	var items []envVarItem
	if err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/services/%s/env-vars?limit=100", serviceID), nil, &items); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, it := range items {
		out[it.EnvVar.Key] = it.EnvVar.Value
	}
	return out, nil
}

// SetEnvVar adds or updates one variable, leaving every other one alone.
//
// Render also offers a whole-environment PUT. upkeep does not use it: one
// wrong call there wipes the variables the platform set itself, and a tool that
// can silently delete DATABASE_URL is not one to trust with a production
// service.
func (c *Client) SetEnvVar(ctx context.Context, serviceID, key, value string) error {
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/services/%s/env-vars/%s", serviceID, key),
		map[string]string{"value": value}, nil)
}

// Deploy triggers a deploy, optionally of a specific image.
func (c *Client) Deploy(ctx context.Context, serviceID, image string) (string, error) {
	body := map[string]string{}
	if image != "" {
		body["imageUrl"] = image
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/services/%s/deploys", serviceID), body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// DeployStatus is where a deploy got to. Render reports several distinct
// failures and they are worth keeping apart: a build that never compiled and a
// deploy that could not start are different problems.
type DeployStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Live reports the one status that means the new code is serving traffic.
func (d DeployStatus) Live() bool { return d.Status == "live" }

// Failed reports the statuses a deploy never leaves.
func (d DeployStatus) Failed() bool {
	switch d.Status {
	case "build_failed", "update_failed", "pre_deploy_failed", "canceled", "deactivated":
		return true
	}
	return false
}

func (c *Client) DeployStatus(ctx context.Context, serviceID, deployID string) (DeployStatus, error) {
	var out DeployStatus
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/services/%s/deploys/%s", serviceID, deployID), nil, &out)
	return out, err
}

// Service is the subset upkeep reads.
type Service struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *Client) Service(ctx context.Context, serviceID string) (Service, error) {
	var s Service
	err := c.do(ctx, http.MethodGet, "/services/"+serviceID, nil, &s)
	return s, err
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
		return fmt.Errorf("render %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("render %s: read body: %w", path, err)
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
			return fmt.Errorf("render %s: decode result: %w", path, err)
		}
	}
	return nil
}
