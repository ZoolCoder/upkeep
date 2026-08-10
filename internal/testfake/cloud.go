// Package testfake is a whole cloud on localhost.
//
// It answers Render, Cloudflare and Neon from one HTTP server, in the envelope
// shapes those APIs really use, so a test can drive the actual binary end to
// end without an account anywhere. Point the three base-URL variables at it and
// upkeep cannot tell the difference.
//
// It exists because "easy to test" has to mean something concrete: a
// contributor with no cloud access, and a pull request from a fork with no
// secrets, must both be able to run the whole suite.
package testfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Cloud is a fake Render + Cloudflare + Neon. Every field is safe to read after
// a run and safe to set before one.
type Cloud struct {
	server *httptest.Server

	mu sync.Mutex

	// Env is the Render service's environment, keyed by service id.
	Env map[string]map[string]string
	// Deploys counts triggered deploys, and DeployStatuses is walked one entry
	// per poll so a test can say "building, building, live".
	Deploys        int
	DeployStatuses []string
	// DeafWrites accepts every write and changes nothing — a provider that
	// normalises, defers, or simply lies.
	DeafWrites bool

	// Buckets that exist, and their two settings.
	Buckets    map[string]bool
	PublicR2   map[string]bool
	CORS       map[string]json.RawMessage
	PagesProj  map[string]json.RawMessage
	NeonBranch map[string][]string

	// Requests is every path the tool asked for, in order, so a test can assert
	// what it did NOT do as well as what it did.
	Requests []string
}

// New starts a fake cloud with nothing configured in it.
func New() *Cloud {
	c := &Cloud{
		Env:        map[string]map[string]string{},
		Buckets:    map[string]bool{},
		PublicR2:   map[string]bool{},
		CORS:       map[string]json.RawMessage{},
		PagesProj:  map[string]json.RawMessage{},
		NeonBranch: map[string][]string{},
	}
	c.server = httptest.NewServer(http.HandlerFunc(c.route))
	return c
}

// URL is the base every provider should be pointed at.
func (c *Cloud) URL() string { return c.server.URL }

// Environ returns the variables that aim upkeep at this fake, ready to hand to
// a test's environment.
func (c *Cloud) Environ() map[string]string {
	return map[string]string{
		"RENDER_API_URL":       c.URL(),
		"CLOUDFLARE_API_URL":   c.URL(),
		"NEON_API_URL":         c.URL(),
		"RENDER_API_KEY":       "fake",
		"CLOUDFLARE_API_TOKEN": "fake",
		"NEON_API_KEY":         "fake",
	}
}

func (c *Cloud) Close() { c.server.Close() }

// Asked reports whether any request touched a path containing needle.
func (c *Cloud) Asked(needle string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.Requests {
		if strings.Contains(r, needle) {
			return true
		}
	}
	return false
}

func (c *Cloud) route(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.Requests = append(c.Requests, r.Method+" "+r.URL.Path)
	c.mu.Unlock()

	switch {
	case strings.HasPrefix(r.URL.Path, "/services/"):
		c.render(w, r)
	case strings.HasPrefix(r.URL.Path, "/accounts/"):
		c.cloudflare(w, r)
	case strings.HasPrefix(r.URL.Path, "/projects/"):
		c.neon(w, r)
	default:
		http.NotFound(w, r)
	}
}

// --- Render: plain JSON, no envelope ---

func (c *Cloud) render(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	service := parts[1]

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Env[service] == nil {
		c.Env[service] = map[string]string{}
	}

	switch {
	case len(parts) == 3 && parts[2] == "env-vars" && r.Method == http.MethodGet:
		var out []map[string]any
		for k, v := range c.Env[service] {
			out = append(out, map[string]any{"envVar": map[string]string{"key": k, "value": v}})
		}
		writeJSON(w, 200, out)

	case len(parts) == 4 && parts[2] == "env-vars" && r.Method == http.MethodPut:
		var body struct {
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !c.DeafWrites {
			c.Env[service][parts[3]] = body.Value
		}
		writeJSON(w, 200, map[string]string{"key": parts[3]})

	case len(parts) == 3 && parts[2] == "deploys" && r.Method == http.MethodPost:
		c.Deploys++
		writeJSON(w, 201, map[string]string{"id": "dep-1"})

	case len(parts) == 4 && parts[2] == "deploys" && r.Method == http.MethodGet:
		status := "live"
		if len(c.DeployStatuses) > 0 {
			status = c.DeployStatuses[0]
			if len(c.DeployStatuses) > 1 {
				c.DeployStatuses = c.DeployStatuses[1:]
			}
		}
		writeJSON(w, 200, map[string]string{"id": parts[3], "status": status})

	default:
		http.NotFound(w, r)
	}
}

// --- Cloudflare: the success/result envelope ---

func (c *Cloud) cloudflare(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case strings.Contains(path, "/r2/buckets/") && strings.HasSuffix(path, "/cors"):
		bucket := between(path, "/r2/buckets/", "/cors")
		if r.Method == http.MethodPut {
			body, _ := readRaw(r)
			if !c.DeafWrites {
				c.CORS[bucket] = body
			}
			cfOK(w, map[string]string{})
			return
		}
		rule, ok := c.CORS[bucket]
		if !ok {
			cfError(w, 404, 10059, "The CORS configuration does not exist.")
			return
		}
		cfRaw(w, rule)

	case strings.Contains(path, "/r2/buckets/") && strings.HasSuffix(path, "/domains/managed"):
		bucket := between(path, "/r2/buckets/", "/domains/managed")
		if r.Method == http.MethodPut {
			if !c.DeafWrites {
				c.PublicR2[bucket] = true
			}
			cfOK(w, map[string]string{})
			return
		}
		cfOK(w, map[string]any{"enabled": c.PublicR2[bucket], "domain": "pub-x.r2.dev"})

	case strings.Contains(path, "/r2/buckets/") && r.Method == http.MethodGet:
		bucket := path[strings.Index(path, "/r2/buckets/")+len("/r2/buckets/"):]
		if !c.Buckets[bucket] {
			cfError(w, 404, 10006, "Bucket not found.")
			return
		}
		cfOK(w, map[string]string{"name": bucket})

	case strings.HasSuffix(path, "/r2/buckets") && r.Method == http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !c.DeafWrites {
			c.Buckets[body.Name] = true
		}
		cfOK(w, map[string]string{"name": body.Name})

	case strings.Contains(path, "/pages/projects/"):
		name := path[strings.Index(path, "/pages/projects/")+len("/pages/projects/"):]
		if r.Method == http.MethodPatch {
			body, _ := readRaw(r)
			if !c.DeafWrites {
				c.PagesProj[name] = mergePages(c.PagesProj[name], body)
			}
			cfOK(w, map[string]string{})
			return
		}
		project, ok := c.PagesProj[name]
		if !ok {
			cfError(w, 404, 8000007, "Project not found.")
			return
		}
		cfRaw(w, project)

	default:
		cfError(w, 404, 7003, "no route for "+path)
	}
}

// --- Neon: plain JSON ---

func (c *Cloud) neon(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	project := between(r.URL.Path, "/projects/", "/branches")
	branches, ok := c.NeonBranch[project]
	if !ok {
		writeJSON(w, 404, map[string]string{"message": "project not found"})
		return
	}
	var out []map[string]any
	for _, b := range branches {
		out = append(out, map[string]any{"id": "br-" + b, "name": b})
	}
	writeJSON(w, 200, map[string]any{"branches": out})
}

// --- helpers ---

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

func readRaw(r *http.Request) (json.RawMessage, error) {
	var raw json.RawMessage
	err := json.NewDecoder(r.Body).Decode(&raw)
	return raw, err
}

// mergePages applies a PATCH to whatever the project already was, because
// Cloudflare's PATCH is partial and a fake that replaced the whole object would
// hide a caller sending too little.
func mergePages(existing, patch json.RawMessage) json.RawMessage {
	current := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &current)
	}
	incoming := map[string]any{}
	_ = json.Unmarshal(patch, &incoming)
	for k, v := range incoming {
		current[k] = v
	}
	merged, _ := json.Marshal(current)
	return merged
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func cfOK(w http.ResponseWriter, result any) {
	writeJSON(w, 200, map[string]any{
		"success": true, "result": result, "errors": []any{}, "messages": []any{},
	})
}

func cfRaw(w http.ResponseWriter, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = fmt.Fprintf(w,
		`{"success":true,"result":%s,"errors":[],"messages":[]}`, string(result))
}

func cfError(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, map[string]any{
		"success": false, "result": nil,
		"errors":   []any{map[string]any{"code": code, "message": message}},
		"messages": []any{},
	})
}
