package neonapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const apiKey = "a-real-looking-neon-key"

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
	c := New("https://console.example", apiKey)
	for _, rendered := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), c.String()} {
		if strings.Contains(rendered, apiKey) {
			t.Fatalf("the key reached the output: %s", rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Errorf("it should say it was redacted, got %q", rendered)
		}
	}
}

func TestBranchesAreRead(t *testing.T) {
	c := serving(t, 200, `{"branches":[
		{"id":"br-1","name":"main","default":true},
		{"id":"br-2","name":"dev","default":false}]}`)

	got, err := c.Branches(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "main" || !got[0].Default {
		t.Fatalf("got %+v", got)
	}
}

func TestAProjectIsRead(t *testing.T) {
	c := serving(t, 200, `{"project":{"id":"p1","name":"zoolaqar"}}`)
	got, err := c.Project(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "zoolaqar" {
		t.Errorf("got %+v", got)
	}
}

// Neon reports failures as a status plus a message, with no envelope, so the
// message has to be lifted out of the body rather than shown as raw JSON.
func TestAFailureCarriesNeonsOwnMessage(t *testing.T) {
	c := serving(t, 404, `{"message":"project not found","code":""}`)

	_, err := c.Branches(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a typed error, got %T", err)
	}
	if apiErr.Status != 404 || !strings.Contains(apiErr.Message, "project not found") {
		t.Errorf("got %+v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "404") || !strings.Contains(apiErr.Error(), "/projects/nope") {
		t.Errorf("the message should carry both, got %q", apiErr.Error())
	}
}

// A body with no message field at all still has to produce something a human
// can act on.
func TestAFailureWithNoMessageFallsBackToTheBody(t *testing.T) {
	c := serving(t, 500, "upstream exploded")
	_, err := c.Branches(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("got %v", err)
	}
}

func TestAMalformedSuccessBodyIsADecodeError(t *testing.T) {
	c := serving(t, 200, `{"branches": not-json}`)
	if _, err := c.Branches(context.Background(), "p1"); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestAnUnreachableHost(t *testing.T) {
	c := New("http://127.0.0.1:0", apiKey)
	if _, err := c.Branches(context.Background(), "p1"); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	c := serving(t, 200, `{"branches":[]}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Branches(ctx, "p1"); err == nil {
		t.Fatal("a cancelled context must not complete")
	}
}

func TestTheDefaultBaseUrlIsNeon(t *testing.T) {
	if got := New("", apiKey).String(); !strings.Contains(got, "console.neon.tech") {
		t.Errorf("got %q", got)
	}
}

func TestTheKeyIsSentAsABearer(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"branches":[]}`)
	}))
	defer server.Close()

	if _, err := New(server.URL, apiKey).Branches(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer "+apiKey {
		t.Errorf("got %q", seen)
	}
}

// upkeep only ever reads from Neon: a database is not a thing to converge
// silently, and the transport should offer no way to write one.
func TestTheClientOffersNoWrites(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = fmt.Fprint(w, `{"branches":[],"project":{"id":"p"}}`)
	}))
	defer server.Close()

	c := New(server.URL, apiKey)
	if _, err := c.Branches(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Project(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("the Neon client issued a %s", m)
		}
	}
}
