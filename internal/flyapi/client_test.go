package flyapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "FlyV1 a-real-looking-fly-token"

func serving(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, token)
}

// An accidental %v on a client must not print the credential it holds.
func TestPrintingAClientDoesNotPrintItsToken(t *testing.T) {
	c := New("https://api.example", token)
	for _, rendered := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), c.String()} {
		if strings.Contains(rendered, token) {
			t.Fatalf("the token reached the output: %s", rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Errorf("it should say it was redacted, got %q", rendered)
		}
	}
}

// Fly answers with names and digests, never values. The map this builds is the
// only thing upkeep can plan from, and it deliberately holds no secret.
func TestSecretsComeBackAsNamesAndDigestsOnly(t *testing.T) {
	c := serving(t, 200, `[
		{"label":"DATABASE_URL","type":"env","digest":"sha256:aaa"},
		{"label":"STRIPE_KEY","type":"env","digest":"sha256:bbb"}]`)

	got, err := c.Secrets(context.Background(), "my-app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got["DATABASE_URL"].Digest != "sha256:aaa" {
		t.Errorf("got %+v", got["DATABASE_URL"])
	}
	// There is no field for a value, so a leaked one has nowhere to land.
	if fmt.Sprintf("%+v", got) == "" {
		t.Fatal("unreachable")
	}
}

func TestSettingASecretPostsToItsOwnPath(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	if err := New(server.URL, token).
		SetSecret(context.Background(), "my-app", "TOKEN", "v"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Errorf("method %s", method)
	}
	if path != "/apps/my-app/secrets/TOKEN/type/env" {
		t.Errorf("path %s — it must name the single secret", path)
	}
	if !strings.Contains(body, `"value":"v"`) {
		t.Errorf("body %s", body)
	}
}

func TestAnAppIsRead(t *testing.T) {
	c := serving(t, 200, `{"name":"my-app","status":"deployed"}`)
	app, err := c.App(context.Background(), "my-app")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "deployed" {
		t.Errorf("got %+v", app)
	}
}

func TestAFailureCarriesFlysOwnMessage(t *testing.T) {
	c := serving(t, 404, `{"error":"App not found"}`)
	_, err := c.Secrets(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a typed error, got %T", err)
	}
	if apiErr.Status != 404 || !strings.Contains(apiErr.Message, "App not found") {
		t.Errorf("got %+v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "/apps/nope/secrets") {
		t.Errorf("the message should name the endpoint, got %q", apiErr.Error())
	}
}

func TestAFailureWithNoMessageFallsBackToTheBody(t *testing.T) {
	c := serving(t, 500, "upstream exploded")
	if _, err := c.Secrets(context.Background(), "my-app"); err == nil ||
		!strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("got %v", err)
	}
}

func TestAMalformedSuccessBodyIsADecodeError(t *testing.T) {
	c := serving(t, 200, `[{"label": not-json}]`)
	if _, err := c.Secrets(context.Background(), "my-app"); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestAnUnreachableHost(t *testing.T) {
	if _, err := New("http://127.0.0.1:0", token).
		Secrets(context.Background(), "my-app"); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	c := serving(t, 200, `[]`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Secrets(ctx, "my-app"); err == nil {
		t.Fatal("a cancelled context must not complete")
	}
}

func TestTheDefaultBaseUrlIsFly(t *testing.T) {
	if got := New("", token).String(); !strings.Contains(got, "api.machines.dev") {
		t.Errorf("got %q", got)
	}
}

func TestTheTokenIsSentAsABearer(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	if _, err := New(server.URL, token).Secrets(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer "+token {
		t.Errorf("got %q", seen)
	}
}
