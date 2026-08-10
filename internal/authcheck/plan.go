// Package authcheck asserts that an app's authentication configuration is
// coherent, and proves the parts that can be proved.
//
// Every other surface here checks one system against a config. This one checks
// several variables against each other, because that is where authentication
// actually fails: not a missing value, but two values that disagree.
//
// The failures it was written from, all of which were invisible until someone
// tried to sign in:
//
//   - an auth URL that lost its path, so every session lookup 404'd — and a
//     404 on get-session reads exactly like "no session"
//   - a CORS allowlist that matches an origin exactly, so a preview deployment
//     loaded with an empty page and no error
//   - an unset provider variable, silently inferred as a different provider
//   - a sign-in page offering three ways in that the backend did not have
//
// It changes nothing. Authentication is the one surface where a wrong
// automatic fix locks out everybody, including whoever ran the tool.
package authcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

// Fetcher reads a URL. Injected so tests need no network.
type Fetcher interface {
	Get(ctx context.Context, url string) (status int, body []byte, err error)
}

// HTTP is the real one.
type HTTP struct{ Client *http.Client }

func (h HTTP) Get(ctx context.Context, url string) (int, []byte, error) {
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()

	// Bounded: a misconfigured URL often returns an entire SPA, and reading it
	// all to decide it is not a JWKS is wasteful.
	body := make([]byte, 64*1024)
	n, _ := res.Body.Read(body)
	return res.StatusCode, body[:n], nil
}

// Plan returns what does not cohere. Every action is MANUAL: this surface
// reports and never changes anything.
func Plan(ctx context.Context, a config.Auth, env map[string]string, fetch Fetcher) (plan.Plan, error) {
	var out plan.Plan
	say := func(target, detail string) {
		out.Add(plan.Action{Op: plan.OpManual, Resource: "auth", Target: target, Detail: detail})
	}

	provider := env["AUTH_PROVIDER"]
	if provider == "" {
		say("AUTH_PROVIDER",
			"not set on the service, so the provider is inferred from older variables — "+
				"an unset variable quietly selecting a different provider is how a sign-in "+
				"stops working with nothing in the logs")
	}

	// The frontend is built with its provider baked in, so it cannot be read
	// back from anywhere. Declaring it is the only way to check the two halves
	// agree.
	if a.FrontendProvider != "" && provider != "" {
		if want := backendFor(a.FrontendProvider); want != "" && want != provider {
			say("provider", fmt.Sprintf(
				"the frontend was built for %q, which needs AUTH_PROVIDER=%s, but the service says %q — "+
					"every sign-in fails and the page shows only that it did",
				a.FrontendProvider, want, provider))
		}
	}

	checkDevBypass(env, say)
	checkOrigins(a, env, say)
	checkIssuer(ctx, provider, env, fetch, say)
	checkJWKS(ctx, provider, env, fetch, say)
	checkFrontendAuthURL(ctx, a, fetch, say)

	return out, nil
}

// backendFor maps what the frontend was built with to what the service must
// say. They are deliberately different vocabularies — a browser talks to an
// issuer, a server verifies what it signed — so the mapping is explicit.
func backendFor(frontend string) string {
	switch strings.ToLower(frontend) {
	case "local":
		return "local"
	case "neon", "oidc", "supabase":
		return "remote"
	case "fake":
		return "dev"
	}
	return ""
}

func checkDevBypass(env map[string]string, say func(string, string)) {
	if env["AUTH_DEV_BYPASS"] == "true" && env["APP_ENV"] == "production" {
		say("AUTH_DEV_BYPASS",
			"true on a production service — every request is whoever it claims to be")
	}
}

// checkOrigins is the preview-URL failure: the allowlist matches an origin
// exactly, so a hostname that is nearly right is entirely wrong.
func checkOrigins(a config.Auth, env map[string]string, say func(string, string)) {
	if len(a.SiteOrigins) == 0 {
		return
	}
	allowed := map[string]bool{}
	for _, o := range strings.Split(env["CORS_ORIGINS"], ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[strings.TrimSuffix(o, "/")] = true
		}
	}
	for _, want := range a.SiteOrigins {
		if !allowed[strings.TrimSuffix(want, "/")] {
			say("CORS_ORIGINS", fmt.Sprintf(
				"does not allow %s — the allowlist is an exact origin match, so the site "+
					"loads and every API call fails with nothing shown", want))
		}
	}
}

// checkIssuer proves rather than guesses.
//
// It first compared AUTH_ISSUER against APP_PUBLIC_URL, which is wrong: the
// public URL is the SITE, used to build invite links, while the issuer is the
// API that signs the tokens. Those are different hosts in any deployment with a
// separate frontend, so the check fired on a correct configuration.
//
// When this app signs its own tokens it also serves its own keys, so the issuer
// can be verified by asking it for them.
func checkIssuer(ctx context.Context, provider string, env map[string]string, fetch Fetcher, say func(string, string)) {
	if provider != "local" {
		return
	}
	issuer := env["AUTH_ISSUER"]
	if issuer == "" {
		say("AUTH_ISSUER", "not set, and this app signs its own tokens — they will not verify")
		return
	}
	probe := strings.TrimSuffix(issuer, "/") + "/auth/.well-known/jwks.json"
	status, body, err := fetch.Get(ctx, probe)
	switch {
	case err != nil:
		say("AUTH_ISSUER", fmt.Sprintf("%s could not be reached: %v", probe, err))
	case status != http.StatusOK:
		say("AUTH_ISSUER", fmt.Sprintf(
			"%s answered %d — the issuer names a host that does not serve this app's keys, "+
				"so a token it signed cannot be verified", probe, status))
	case !hasKeys(body):
		say("AUTH_ISSUER", fmt.Sprintf("%s served no usable keys", probe))
	}
}

func hasKeys(body []byte) bool {
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	return json.Unmarshal(body, &doc) == nil && len(doc.Keys) > 0
}

// checkJWKS proves the keys are actually served. A URL being set is not
// evidence it resolves, and the usual failure is a host that answers every
// unknown path with a page rather than a 404.
func checkJWKS(ctx context.Context, provider string, env map[string]string, fetch Fetcher, say func(string, string)) {
	uri := env["AUTH_JWKS_URI"]
	switch {
	case provider == "remote" && uri == "":
		say("AUTH_JWKS_URI", "not set, and the provider is remote — no token can be verified")
		return
	case uri == "":
		return
	}

	status, body, err := fetch.Get(ctx, uri)
	if err != nil {
		say("AUTH_JWKS_URI", fmt.Sprintf("%s could not be reached: %v", uri, err))
		return
	}
	if status != http.StatusOK {
		say("AUTH_JWKS_URI", fmt.Sprintf("%s answered %d", uri, status))
		return
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		say("AUTH_JWKS_URI", fmt.Sprintf(
			"%s answered 200 but not JSON — a host that serves a page for every unknown "+
				"path looks exactly like this", uri))
		return
	}
	if len(doc.Keys) == 0 {
		say("AUTH_JWKS_URI", fmt.Sprintf("%s is valid JSON with no keys in it", uri))
	}
}

// checkFrontendAuthURL is the lost-path failure. Better Auth keeps a path it is
// given and otherwise falls back to /api/auth, which both hosts answer with a
// 404 — and a 404 on get-session reads exactly like "no session".
func checkFrontendAuthURL(ctx context.Context, a config.Auth, fetch Fetcher, say func(string, string)) {
	if a.FrontendAuthURL == "" {
		return
	}
	probe := strings.TrimSuffix(a.FrontendAuthURL, "/") + "/get-session"
	status, body, err := fetch.Get(ctx, probe)
	if err != nil {
		say("frontendAuthUrl", fmt.Sprintf("%s could not be reached: %v", probe, err))
		return
	}
	if status == http.StatusNotFound {
		say("frontendAuthUrl", fmt.Sprintf(
			"%s answers 404 — the client reads that as 'no session' and every sign-in "+
				"appears to succeed and then not have happened", probe))
		return
	}
	if status == http.StatusOK && !json.Valid(trimmed(body)) {
		say("frontendAuthUrl", fmt.Sprintf(
			"%s answered 200 with something that is not JSON, which is what a single-page "+
				"app returns for a path it does not know", probe))
	}
}

func trimmed(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
