package config

import (
	"strings"
	"testing"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

const minimal = `
version: 1
apps:
  - name: zoolaqar
    render:
      serviceId: srv-abc
      env:
        - key: R2_BUCKET
          value: zoolaqar
`

func TestAValidConfigLoads(t *testing.T) {
	cfg, err := Parse([]byte(minimal), env())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Render.ServiceID != "srv-abc" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

// An unset ${VAR} must not become an empty string: an empty accountId reaches
// the API as a malformed path and returns a confusing 404.
func TestAnUnsetReferenceIsAnError(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    r2:
      accountId: ${CF_ACCOUNT}
      bucket: b
`), env())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "CF_ACCOUNT") {
		t.Errorf("the error should name the variable, got %v", err)
	}
}

func TestAReferenceIsExpanded(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
apps:
  - name: a
    r2:
      accountId: ${CF_ACCOUNT}
      bucket: b
`), env("CF_ACCOUNT", "acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps[0].R2.AccountID != "acct-1" {
		t.Errorf("got %q", cfg.Apps[0].R2.AccountID)
	}
}

// A mistyped key is a setting that silently does nothing, which on this tool
// means a variable nobody notices is unset.
func TestAnUnknownKeyIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      enviroment: []
`), env())
	if err == nil {
		t.Fatal("expected an error for the typo")
	}
}

func TestAVariableWithNoSourceIsRejected(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      env:
        - key: R2_SECRET_ACCESS_KEY
`), env())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "empty string") {
		t.Errorf("the error should say what would happen, got %v", err)
	}
}

// "Something is missing" without "here is what to do" is not worth printing.
func TestAManualVariableMustExplainItself(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      env:
        - key: FEE_ACCOUNT_INFO
          manual: true
`), env())
	if err == nil || !strings.Contains(err.Error(), "why") {
		t.Fatalf("expected a why to be required, got %v", err)
	}
}

func TestOriginsMustBeWholeOrigins(t *testing.T) {
	for _, origin := range []string{"zoolaqar.pages.dev", "https://zoolaqar.pages.dev/"} {
		_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    r2:
      accountId: acct
      bucket: b
      cors:
        - origins: ["`+origin+`"]
          methods: ["PUT"]
`), env())
		if err == nil {
			t.Errorf("origin %q should be rejected", origin)
		}
	}
}

func TestAnAppWithNoSurfacesIsRejected(t *testing.T) {
	_, err := Parse([]byte("version: 1\napps:\n  - name: a\n"), env())
	if err == nil || !strings.Contains(err.Error(), "nothing to reconcile") {
		t.Fatalf("got %v", err)
	}
}

func TestAServiceIdIsCheckedForShape(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: zoolaqar-backend
`), env())
	if err == nil || !strings.Contains(err.Error(), "srv-") {
		t.Fatalf("got %v", err)
	}
}

// The likeliest way to leak a secret with this tool is to paste one into a
// value: and commit the file. That has to fail before the file exists, not
// after somebody notices it in a diff.
func TestALiteralUnderACredentialShapedNameIsRefused(t *testing.T) {
	for _, key := range []string{
		"R2_SECRET_ACCESS_KEY", "AUTH_SIGNING_KEY", "DATABASE_URL",
		"RESEND_API_KEY", "SOME_TOKEN", "db_password", "VAPID_PRIVATE_KEY",
	} {
		_, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      env:
        - key: `+key+`
          value: hunter2
`), env())
		if err == nil {
			t.Errorf("%s with a literal should be refused", key)
			continue
		}
		if !strings.Contains(err.Error(), "valueEnv") {
			t.Errorf("%s: the error should name the field to use instead, got %v", key, err)
		}
	}
}

// The escape hatch is never "commit it anyway" — it is to name the source.
func TestTheSameKeyIsFineAsAReference(t *testing.T) {
	for _, entry := range []string{
		"          valueEnv: R2_SECRET_ACCESS_KEY",
		"          manual: true\n          why: minted in the dashboard",
	} {
		if _, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      env:
        - key: R2_SECRET_ACCESS_KEY
`+entry+`
`), env()); err != nil {
			t.Errorf("%q should be accepted, got %v", entry, err)
		}
	}
}

// Ordinary configuration must not be caught by the heuristic, or people learn
// to work around the check rather than with it.
func TestPlainValuesAreStillPlain(t *testing.T) {
	for _, key := range []string{
		"R2_BUCKET", "R2_ENDPOINT", "R2_PUBLIC_BASE", "APP_ENV",
		"CORS_ORIGINS", "VISIT_FEE_SDG", "TZ", "AUTH_PROVIDER", "AUTH_ISSUER",
	} {
		if _, err := Parse([]byte(`
version: 1
apps:
  - name: a
    render:
      serviceId: srv-a
      env:
        - key: `+key+`
          value: something
`), env()); err != nil {
			t.Errorf("%s should be allowed a literal, got %v", key, err)
		}
	}
}
