package authcheck

import (
	"context"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

// fakeWeb answers a canned status and body per URL.
type fakeWeb struct {
	status map[string]int
	body   map[string]string
	err    error
}

func (f fakeWeb) Get(_ context.Context, url string) (int, []byte, error) {
	if f.err != nil {
		return 0, nil, f.err
	}
	status, ok := f.status[url]
	if !ok {
		status = 200
	}
	return status, []byte(f.body[url]), nil
}

const (
	jwksURL   = "https://auth.example/.well-known/jwks.json"
	ownKeys   = "https://api.example/auth/.well-known/jwks.json"
	realKeyed = `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"abc"}]}`
)

func web() fakeWeb {
	return fakeWeb{
		status: map[string]int{},
		body: map[string]string{
			jwksURL: realKeyed,
			ownKeys: realKeyed,
		},
	}
}

func healthy() map[string]string {
	return map[string]string{
		"AUTH_PROVIDER":    "local",
		"AUTH_ISSUER":      "https://api.example",
		"APP_PUBLIC_URL":   "https://api.example",
		"AUTH_JWKS_URI":    jwksURL,
		"CORS_ORIGINS":     "https://site.example",
		"APP_ENV":          "production",
		"AUTH_SIGNING_KEY": "seed",
	}
}

func planFor(t *testing.T, a config.Auth, env map[string]string, w Fetcher) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), a, env, w)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func complaint(p plan.Plan, target string) (plan.Action, bool) {
	for _, a := range p.Actions {
		if a.Target == target {
			return a, true
		}
	}
	return plan.Action{}, false
}

func TestACoherentSetupSaysNothing(t *testing.T) {
	p := planFor(t, config.Auth{
		FrontendProvider: "local",
		SiteOrigins:      []string{"https://site.example"},
	}, healthy(), web())
	if !p.Empty() {
		t.Fatalf("expected silence, got %+v", p.Actions)
	}
}

// This surface reports and never changes anything: a wrong automatic fix locks
// out everybody, including whoever ran the tool.
func TestEveryFindingIsManual(t *testing.T) {
	env := healthy()
	delete(env, "AUTH_PROVIDER")
	env["AUTH_DEV_BYPASS"] = "true"

	p := planFor(t, config.Auth{SiteOrigins: []string{"https://missing.example"}}, env, web())
	if len(p.Actions) == 0 {
		t.Fatal("expected findings")
	}
	for _, a := range p.Actions {
		if a.Op != plan.OpManual || a.Do != nil {
			t.Errorf("auth must never plan a change: %+v", a)
		}
	}
}

// The frontend is built with its provider baked in, so nothing can read it
// back. Two halves built for different providers fail every sign-in.
func TestAFrontendAndBackendBuiltForDifferentProviders(t *testing.T) {
	env := healthy()
	env["AUTH_PROVIDER"] = "remote"

	p := planFor(t, config.Auth{FrontendProvider: "local"}, env, web())
	a, ok := complaint(p, "provider")
	if !ok {
		t.Fatalf("expected a provider complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "AUTH_PROVIDER=local") {
		t.Errorf("it should say what the service needs, got %q", a.Detail)
	}
}

func TestNeonFrontendNeedsARemoteBackend(t *testing.T) {
	env := healthy()
	env["AUTH_PROVIDER"] = "remote"
	p := planFor(t, config.Auth{FrontendProvider: "neon"}, env, web())
	if _, complained := complaint(p, "provider"); complained {
		t.Errorf("neon↔remote is the correct pairing: %+v", p.Actions)
	}
}

// An unset provider is inferred, and an inferred provider is a different one.
func TestAnUnsetProviderIsReported(t *testing.T) {
	env := healthy()
	delete(env, "AUTH_PROVIDER")
	p := planFor(t, config.Auth{}, env, web())
	if _, ok := complaint(p, "AUTH_PROVIDER"); !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
}

// The allowlist is an exact origin match, so a hostname that is nearly right is
// entirely wrong — the site loads and every API call fails silently.
func TestAnOriginMissingFromTheAllowlist(t *testing.T) {
	p := planFor(t, config.Auth{SiteOrigins: []string{"https://site.example", "https://www.site.example"}},
		healthy(), web())
	a, ok := complaint(p, "CORS_ORIGINS")
	if !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "www.site.example") {
		t.Errorf("it should name the missing origin, got %q", a.Detail)
	}
}

func TestATrailingSlashIsNotADifference(t *testing.T) {
	env := healthy()
	env["CORS_ORIGINS"] = "https://site.example/"
	p := planFor(t, config.Auth{SiteOrigins: []string{"https://site.example"}}, env, web())
	if _, complained := complaint(p, "CORS_ORIGINS"); complained {
		t.Errorf("a trailing slash is the same origin: %+v", p.Actions)
	}
}

func TestDevBypassOnProductionIsReported(t *testing.T) {
	env := healthy()
	env["AUTH_DEV_BYPASS"] = "true"
	p := planFor(t, config.Auth{}, env, web())
	a, ok := complaint(p, "AUTH_DEV_BYPASS")
	if !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "whoever it claims to be") {
		t.Errorf("it should say what it means, got %q", a.Detail)
	}
}

// A URL being set is not evidence it resolves. The usual failure is a host that
// answers every unknown path with a page.
func TestAJwksUrlThatServesAPageInsteadOfKeys(t *testing.T) {
	w := web()
	w.body[jwksURL] = "<!doctype html><html><body>Not found</body></html>"
	p := planFor(t, config.Auth{}, healthy(), w)
	a, ok := complaint(p, "AUTH_JWKS_URI")
	if !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "not JSON") {
		t.Errorf("got %q", a.Detail)
	}
}

func TestAJwksUrlWithNoKeysInIt(t *testing.T) {
	w := web()
	w.body[jwksURL] = `{"keys":[]}`
	p := planFor(t, config.Auth{}, healthy(), w)
	if _, ok := complaint(p, "AUTH_JWKS_URI"); !ok {
		t.Fatalf("valid JSON with no keys verifies nothing: %+v", p.Actions)
	}
}

func TestARemoteProviderWithNoJwksAtAll(t *testing.T) {
	env := healthy()
	env["AUTH_PROVIDER"] = "remote"
	delete(env, "AUTH_JWKS_URI")
	p := planFor(t, config.Auth{}, env, web())
	if _, ok := complaint(p, "AUTH_JWKS_URI"); !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
}

// The lost-path failure: a 404 on get-session reads exactly like "no session",
// so every sign-in appears to succeed and then not have happened.
func TestAnAuthUrlThatLostItsPath(t *testing.T) {
	const probe = "https://api.example/auth/get-session"
	w := web()
	w.status[probe] = 404

	p := planFor(t, config.Auth{FrontendAuthURL: "https://api.example/auth"}, healthy(), w)
	a, ok := complaint(p, "frontendAuthUrl")
	if !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "no session") {
		t.Errorf("it should say how it presents, got %q", a.Detail)
	}
}

func TestAnAuthUrlAnsweringWithASinglePageApp(t *testing.T) {
	const probe = "https://api.example/auth/get-session"
	w := web()
	w.body[probe] = "<!doctype html><html><div id=app></div></html>"

	p := planFor(t, config.Auth{FrontendAuthURL: "https://api.example/auth"}, healthy(), w)
	if _, ok := complaint(p, "frontendAuthUrl"); !ok {
		t.Fatalf("a 200 of HTML is not a session response: %+v", p.Actions)
	}
}

func TestAWorkingAuthUrlSaysNothing(t *testing.T) {
	const probe = "https://api.example/auth/get-session"
	w := web()
	w.body[probe] = `{"session":null,"user":null}`

	p := planFor(t, config.Auth{FrontendAuthURL: "https://api.example/auth"}, healthy(), w)
	if _, complained := complaint(p, "frontendAuthUrl"); complained {
		t.Errorf("a valid JSON answer is fine: %+v", p.Actions)
	}
}

// A local provider signs its own tokens and serves its own keys, so the issuer
// is verified by asking it for them.
//
// This check first compared AUTH_ISSUER against APP_PUBLIC_URL, and fired on a
// correct configuration: the public URL is the SITE, used to build invite
// links, while the issuer is the API. Different hosts in any deployment with a
// separate frontend.
func TestAnIssuerThatDoesNotServeThisAppsKeys(t *testing.T) {
	env := healthy()
	env["AUTH_ISSUER"] = "https://somewhere.else"
	w := web()
	w.status["https://somewhere.else/auth/.well-known/jwks.json"] = 404

	p := planFor(t, config.Auth{}, env, w)
	a, ok := complaint(p, "AUTH_ISSUER")
	if !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "cannot be verified") {
		t.Errorf("got %q", a.Detail)
	}
}

// The site and the API are different hosts, and that is normal.
func TestAnIssuerDifferentFromTheSiteUrlIsFine(t *testing.T) {
	env := healthy()
	env["APP_PUBLIC_URL"] = "https://site.example"
	env["AUTH_ISSUER"] = "https://api.example"

	p := planFor(t, config.Auth{}, env, web())
	if _, complained := complaint(p, "AUTH_ISSUER"); complained {
		t.Errorf("a separate frontend is not a misconfiguration: %+v", p.Actions)
	}
}

func TestALocalProviderWithNoIssuerAtAll(t *testing.T) {
	env := healthy()
	delete(env, "AUTH_ISSUER")
	p := planFor(t, config.Auth{}, env, web())
	if _, ok := complaint(p, "AUTH_ISSUER"); !ok {
		t.Fatalf("expected a complaint, got %+v", p.Actions)
	}
}
