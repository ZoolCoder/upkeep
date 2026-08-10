package renderapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const apiKey = "a-real-looking-render-key"

func serving(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, apiKey)
}

// An accidental %v on a client must not print the credential it holds.
func TestPrintingAClientDoesNotPrintItsKey(t *testing.T) {
	c := New("https://api.example", apiKey)
	for _, rendered := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), c.String()} {
		if strings.Contains(rendered, apiKey) {
			t.Fatalf("the key reached the output: %s", rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Errorf("it should say it was redacted, got %q", rendered)
		}
	}
}

func TestAnEnvironmentIsReadIntoAMap(t *testing.T) {
	c := serving(t, 200, `[
		{"envVar":{"key":"APP_ENV","value":"production"}},
		{"envVar":{"key":"TZ","value":"UTC"}}]`)

	env, err := c.EnvVars(context.Background(), "srv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env["APP_ENV"] != "production" {
		t.Fatalf("got %+v", env)
	}
}

func TestAnEmptyEnvironmentIsNotAnError(t *testing.T) {
	c := serving(t, 200, `[]`)
	env, err := c.EnvVars(context.Background(), "srv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Errorf("got %+v", env)
	}
}

// One variable at a time, never the whole-environment PUT: one wrong call there
// wipes the variables the platform set itself.
func TestSettingAVariableTouchesOnlyThatVariable(t *testing.T) {
	var method, path string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	if err := New(server.URL, apiKey).SetEnvVar(context.Background(), "srv-1", "R2_BUCKET", "b"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut {
		t.Errorf("method %s", method)
	}
	if path != "/services/srv-1/env-vars/R2_BUCKET" {
		t.Errorf("path %s — it must name the single variable", path)
	}
	if !strings.Contains(body, `"value":"b"`) {
		t.Errorf("body %s", body)
	}
}

func TestADeployReturnsItsId(t *testing.T) {
	c := serving(t, 201, `{"id":"dep-9"}`)
	id, err := c.Deploy(context.Background(), "srv-1", "img:1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "dep-9" {
		t.Errorf("got %q", id)
	}
}

// A deploy with no image is a redeploy of what is already there, and must not
// send an empty imageUrl that Render would reject.
func TestADeployWithNoImageSendsNoImageField(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		_, _ = fmt.Fprint(w, `{"id":"dep-1"}`)
	}))
	defer server.Close()

	if _, err := New(server.URL, apiKey).Deploy(context.Background(), "srv-1", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "imageUrl") {
		t.Errorf("an empty image should be omitted, sent %s", body)
	}
}

func TestDeployStatusDistinguishesLiveFromFailed(t *testing.T) {
	live := DeployStatus{Status: "live"}
	if !live.Live() || live.Failed() {
		t.Error("live is live and not failed")
	}
	for _, s := range []string{"build_failed", "update_failed", "pre_deploy_failed", "canceled", "deactivated"} {
		d := DeployStatus{Status: s}
		if !d.Failed() || d.Live() {
			t.Errorf("%s should be failed and not live", s)
		}
	}
	// In progress is neither — the caller keeps waiting.
	for _, s := range []string{"build_in_progress", "update_in_progress", "created"} {
		d := DeployStatus{Status: s}
		if d.Live() || d.Failed() {
			t.Errorf("%s is neither live nor failed", s)
		}
	}
}

func TestAFailureCarriesRendersOwnMessage(t *testing.T) {
	c := serving(t, 403, `{"message":"you do not have access to this service"}`)
	_, err := c.EnvVars(context.Background(), "srv-1")
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a typed error, got %T", err)
	}
	if apiErr.Status != 403 || !strings.Contains(apiErr.Message, "do not have access") {
		t.Errorf("got %+v", apiErr)
	}
}

func TestAFailureWithNoMessageFallsBackToTheBody(t *testing.T) {
	c := serving(t, 500, "gateway exploded")
	if _, err := c.EnvVars(context.Background(), "srv-1"); err == nil ||
		!strings.Contains(err.Error(), "gateway exploded") {
		t.Fatalf("got %v", err)
	}
}

func TestAnUnreachableHost(t *testing.T) {
	c := New("http://127.0.0.1:0", apiKey)
	if _, err := c.EnvVars(context.Background(), "srv-1"); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	c := serving(t, 200, `[]`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.EnvVars(ctx, "srv-1"); err == nil {
		t.Fatal("a cancelled context must not complete")
	}
}

func TestTheDefaultBaseUrlIsRender(t *testing.T) {
	if got := New("", apiKey).String(); !strings.Contains(got, "api.render.com") {
		t.Errorf("got %q", got)
	}
}

func TestTheKeyIsSentAsABearer(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	if _, err := New(server.URL, apiKey).EnvVars(context.Background(), "srv-1"); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer "+apiKey {
		t.Errorf("got %q", seen)
	}
}
