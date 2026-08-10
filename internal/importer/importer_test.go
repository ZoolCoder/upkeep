package importer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
)

type fakeRender struct{ env map[string]string }

func (f fakeRender) EnvVars(context.Context, string) (map[string]string, error) {
	return f.env, nil
}

type fakeCF struct{ get map[string]string }

func (f fakeCF) Do(_ context.Context, _, path string, _, result any) error {
	raw, ok := f.get[path]
	if !ok || result == nil {
		return nil
	}
	return json.Unmarshal([]byte(raw), result)
}

func importOf(t *testing.T, req Request, r Render, cf CF) string {
	t.Helper()
	out, err := Import(context.Background(), req, r, cf)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// parses is the only assertion that matters for an importer: what it wrote has
// to be something the tool can read back.
func parses(t *testing.T, block string) config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte("version: 1\napps:\n"+block), func(string) string { return "x" })
	if err != nil {
		t.Fatalf("the import does not parse back:\n%s\nerror: %v", block, err)
	}
	return cfg
}

// A live secret must never be written to a file. This is the rule the whole
// package exists under: importing an app cannot be the thing that leaks it.
func TestSecretsComeBackAsReferencesNotValues(t *testing.T) {
	const seed = "MC4CAQAwBQYDK2VwBCIEIHhs3Kg7"
	const dsn = "postgres://user:hunter2@host/db"

	block := importOf(t, Request{Name: "app", ServiceID: "srv-1"}, fakeRender{env: map[string]string{
		"AUTH_SIGNING_KEY":     seed,
		"DATABASE_URL":         dsn,
		"R2_SECRET_ACCESS_KEY": "abc123",
		"APP_ENV":              "production",
	}}, nil)

	for _, secret := range []string{seed, dsn, "abc123"} {
		if strings.Contains(block, secret) {
			t.Errorf("the import wrote a secret value:\n%s", block)
		}
	}
	for _, want := range []string{
		"valueEnv: AUTH_SIGNING_KEY",
		"valueEnv: DATABASE_URL",
		"valueEnv: R2_SECRET_ACCESS_KEY",
		"value: production",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("expected %q in:\n%s", want, block)
		}
	}
	parses(t, block)
}

// VISIT_FEE_SDG=5000 found this against the live service: unquoted it decodes
// as an int into a string field, so the config upkeep had just written
// would not load.
func TestValuesYamlWouldMistypeAreQuoted(t *testing.T) {
	block := importOf(t, Request{Name: "app", ServiceID: "srv-1"}, fakeRender{env: map[string]string{
		"VISIT_FEE_SDG": "5000",
		"FEATURE_FLAG":  "true",
		"RATIO":         "1.5",
		"OCTALISH":      "0755",
		"EMPTY":         "",
		"URL":           "https://example.com/a?b=c",
		"TZ":            "Africa/Khartoum",
	}}, nil)

	cfg := parses(t, block)
	got := map[string]string{}
	for _, e := range cfg.Apps[0].Render.Env {
		got[e.Key] = e.Value
	}
	for key, want := range map[string]string{
		"VISIT_FEE_SDG": "5000",
		"FEATURE_FLAG":  "true",
		"RATIO":         "1.5",
		"OCTALISH":      "0755",
		"URL":           "https://example.com/a?b=c",
		"TZ":            "Africa/Khartoum",
	} {
		if got[key] != want {
			t.Errorf("%s round-tripped to %q, want %q", key, got[key], want)
		}
	}
	// An empty value cannot be represented, so it is left unmanaged rather
	// than emitted as an entry validation would reject — but the reader is
	// told, because a silently dropped variable is how a config starts lying.
	if _, present := got["EMPTY"]; present {
		t.Error("an empty variable should not be emitted as an entry")
	}
	if !strings.Contains(block, "set but empty on the service") ||
		!strings.Contains(block, "EMPTY") {
		t.Errorf("the reader should be told what was skipped:\n%s", block)
	}
}

func TestR2AndPagesAreReadFromLiveState(t *testing.T) {
	cf := fakeCF{get: map[string]string{
		"/accounts/acct/r2/buckets/b/domains/managed": `{"enabled":true,"domain":"pub-x.r2.dev"}`,
		"/accounts/acct/r2/buckets/b/cors": `{"rules":[{"allowed":{
			"origins":["https://example.com"],"methods":["PUT"],
			"headers":["content-type","cache-control"]},"maxAgeSeconds":3600}]}`,
		"/accounts/acct/pages/projects/p": `{"name":"p","production_branch":"main",
			"domains":["p.pages.dev","www.example.com"]}`,
	}}

	block := importOf(t, Request{Name: "app", AccountID: "acct", Bucket: "b", Pages: "p"}, nil, cf)
	cfg := parses(t, block)
	app := cfg.Apps[0]

	if !app.R2.Public {
		t.Error("public access was not carried over")
	}
	if len(app.R2.CORS) != 1 || len(app.R2.CORS[0].Headers) != 2 {
		t.Errorf("CORS did not round-trip: %+v", app.R2.CORS)
	}
	if app.Pages.ProductionBranch != "main" {
		t.Errorf("production branch is %q", app.Pages.ProductionBranch)
	}
	if len(app.Pages.Domains) != 2 {
		t.Errorf("domains did not round-trip: %v", app.Pages.Domains)
	}
}

// Importing a surface whose credential is absent must say so rather than
// silently emitting a block with nothing in it.
func TestAMissingCredentialIsNamed(t *testing.T) {
	_, err := Import(context.Background(), Request{Name: "app", ServiceID: "srv-1"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "RENDER_API_KEY") {
		t.Fatalf("got %v", err)
	}
	_, err = Import(context.Background(), Request{Name: "app", AccountID: "a", Bucket: "b"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("got %v", err)
	}
}

func TestAnImportNeedsAName(t *testing.T) {
	if _, err := Import(context.Background(), Request{ServiceID: "srv-1"}, fakeRender{}, nil); err == nil {
		t.Fatal("expected an error")
	}
}

// The importer and the validator must agree exactly. If the importer emitted a
// literal for a key validation refuses, it would write a file the tool itself
// cannot read back — the same class of bug as the unquoted integer, and this
// one would also be a secret sitting in a config.
func TestEveryImportedLiteralSurvivesValidation(t *testing.T) {
	live := map[string]string{}
	for _, key := range []string{
		"R2_SECRET_ACCESS_KEY", "AUTH_SIGNING_KEY", "DATABASE_URL",
		"RESEND_API_KEY", "VAPID_PRIVATE_KEY", "SOME_TOKEN", "db_password",
		"R2_BUCKET", "APP_ENV", "TZ", "VISIT_FEE_SDG", "CORS_ORIGINS",
	} {
		live[key] = "value-of-" + key
	}

	block := importOf(t, Request{Name: "app", ServiceID: "srv-1"}, fakeRender{env: live}, nil)
	cfg := parses(t, block)

	for _, e := range cfg.Apps[0].Render.Env {
		if e.Value != "" && config.LooksSecret(e.Key) {
			t.Errorf("%s was written as a literal despite its name", e.Key)
		}
		if config.LooksSecret(e.Key) && e.ValueEnv == "" {
			t.Errorf("%s should have come back as a reference", e.Key)
		}
	}
}
