package cfapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "a-real-looking-cloudflare-token"

func serving(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, token)
}

// The one test in this file that is about security rather than correctness: an
// accidental %v on a client must not print the credential it holds.
func TestPrintingAClientDoesNotPrintItsToken(t *testing.T) {
	c := New("https://api.example", token)

	for _, rendered := range []string{
		fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%s", c), c.String(),
	} {
		if strings.Contains(rendered, token) {
			t.Fatalf("the token reached the output: %s", rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Errorf("it should say it was redacted, got %q", rendered)
		}
	}
}

func TestASuccessfulCallDecodesTheResult(t *testing.T) {
	c := serving(t, 200, `{"success":true,"result":{"name":"b"},"errors":[],"messages":[]}`)
	var out struct {
		Name string `json:"name"`
	}
	if err := c.Do(context.Background(), "GET", "/x", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "b" {
		t.Errorf("got %q", out.Name)
	}
}

// A result of null is how Cloudflare answers a write. Decoding it into a
// caller's struct would fail; it must be left alone.
func TestANullResultIsNotADecodeFailure(t *testing.T) {
	c := serving(t, 200, `{"success":true,"result":null,"errors":[],"messages":[]}`)
	var out struct {
		Name string `json:"name"`
	}
	if err := c.Do(context.Background(), "PUT", "/x", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}
}

// The envelope decides, not the status: Cloudflare answers 200 with
// success:false, and a transport that trusted the status would return a
// silently empty result.
func TestAFailedEnvelopeIsAnErrorEvenOn200(t *testing.T) {
	c := serving(t, 200, `{"success":false,"result":null,
		"errors":[{"code":10059,"message":"The CORS configuration does not exist."}],"messages":[]}`)

	err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if err == nil {
		t.Fatal("success:false must be an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a typed error, got %T", err)
	}
	if apiErr.Code != 10059 {
		t.Errorf("the code must survive, got %d", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "10059") || !strings.Contains(apiErr.Error(), "/x") {
		t.Errorf("the message should carry both, got %q", apiErr.Error())
	}
}

// A host that answers with a page rather than an envelope is a real failure
// mode — a proxy, a login wall, a wrong base URL.
func TestABodyThatIsNotAnEnvelope(t *testing.T) {
	c := serving(t, 502, "<html><body>Bad Gateway</body></html>")

	err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 502 {
		t.Fatalf("the status should survive, got %v", err)
	}
}

func TestAnUnreachableHost(t *testing.T) {
	// Port 0 is never listening.
	c := New("http://127.0.0.1:0", token)
	if err := c.Do(context.Background(), "GET", "/x", nil, nil); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	c := serving(t, 200, `{"success":true,"result":null,"errors":[],"messages":[]}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Do(ctx, "GET", "/x", nil, nil); err == nil {
		t.Fatal("a cancelled context must not complete")
	}
}

// NotConfigured is what lets a first plan work: an absent CORS policy is the
// normal state of a new bucket, not a failure.
func TestNotConfiguredRecognisesAbsence(t *testing.T) {
	for _, code := range []int{10059, 10006, 10042} {
		if !NotConfigured(&Error{Code: code, Status: 400}) {
			t.Errorf("code %d should read as absence", code)
		}
	}
	if !NotConfigured(&Error{Status: 404}) {
		t.Error("a 404 should read as absence")
	}
	if NotConfigured(&Error{Status: 500, Code: 1}) {
		t.Error("a server error is not absence")
	}
	if NotConfigured(errors.New("plain")) {
		t.Error("a non-API error is not absence")
	}
}

// Forbidden becomes a manual action rather than a crash: the operator can do in
// a dashboard what this token may not.
func TestForbiddenRecognisesPermissionFailures(t *testing.T) {
	for _, e := range []*Error{
		{Status: http.StatusForbidden},
		{Status: http.StatusUnauthorized},
		{Code: 9109, Status: 400},
	} {
		if !Forbidden(e) {
			t.Errorf("%+v should read as forbidden", e)
		}
	}
	if Forbidden(&Error{Status: 404}) {
		t.Error("absence is not a permission failure")
	}
	if Forbidden(errors.New("plain")) {
		t.Error("a non-API error is not a permission failure")
	}
}

// An empty base URL must reach Cloudflare, not a relative path.
func TestTheDefaultBaseUrlIsCloudflare(t *testing.T) {
	if got := New("", token).String(); !strings.Contains(got, "api.cloudflare.com") {
		t.Errorf("got %q", got)
	}
	// A trailing slash would produce a double slash in every path.
	if got := New("https://example.com/", token).String(); strings.Contains(got, "example.com/,") {
		t.Errorf("the trailing slash was not trimmed: %q", got)
	}
}

// A body that cannot be marshalled must fail before a request is sent, not
// halfway through one.
func TestAnUnencodableBodyFailsBeforeSending(t *testing.T) {
	c := serving(t, 200, `{"success":true,"result":null,"errors":[],"messages":[]}`)
	err := c.Do(context.Background(), "PUT", "/x", make(chan int), nil)
	if err == nil || !strings.Contains(err.Error(), "encode") {
		t.Fatalf("got %v", err)
	}
}

func TestTheTokenIsSentAsABearer(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"success":true,"result":null,"errors":[],"messages":[]}`)
	}))
	defer server.Close()

	if err := New(server.URL, token).Do(context.Background(), "GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer "+token {
		t.Errorf("got %q", seen)
	}
}
