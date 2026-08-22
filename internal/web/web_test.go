package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/engine"
	"github.com/zoolcoder/upkeep/internal/renderapi"
	"github.com/zoolcoder/upkeep/internal/testfake"
	"github.com/zoolcoder/zcadmin"
)

// Everything here drives the handler the way a browser would, against the
// same fake cloud the CLI tests use, with a temp directory standing in for
// ~/.local/share/upkeep. No network, no real account.

const twoApps = `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: R2_BUCKET
          value: demo-bucket
        - key: APP_ENV
          value: production
        - key: FEE_ACCOUNT_INFO
          manual: true
          why: real bank details, set by hand on the service
    r2:
      accountId: acc-1
      bucket: demo-bucket
  - name: other
    render:
      serviceId: srv-111111111111111111
      env:
        - key: APP_ENV
          value: production
`

type harness struct {
	t      *testing.T
	cloud  *testfake.Cloud
	srv    *Server
	dir    string
	cookie *http.Cookie
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cloud := testfake.New()
	t.Cleanup(cloud.Close)
	cloud.Env["srv-000000000000000000"] = map[string]string{"APP_ENV": "production"}
	cloud.Env["srv-111111111111111111"] = map[string]string{"APP_ENV": "production"}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "upkeep.yaml")
	if err := os.WriteFile(cfgPath, []byte(twoApps), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := func() engine.Providers {
		return engine.Providers{
			Render: renderapi.New(cloud.URL(), "fake"),
			CF:     cfapi.New(cloud.URL(), "fake"),
		}
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	srv := New(Options{
		ConfigPath:   cfgPath,
		AuthFile:     filepath.Join(dir, "auth.json"),
		ActivityFile: filepath.Join(dir, "activity.jsonl"),
		Providers:    providers,
		Getenv:       func(string) string { return "" },
		Now:          now,
	}).(*Server)
	srv.auth.Sleep = func(time.Duration) {}
	h := &harness{t: t, cloud: cloud, srv: srv, dir: dir}
	h.signIn()
	return h
}

// signIn sets the password through the first-visit form and keeps the cookie.
func (h *harness) signIn() {
	h.t.Helper()
	rec := h.post("/setup", url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}})
	if rec.Code != http.StatusSeeOther {
		h.t.Fatalf("setup: code = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == zcadmin.CookieName {
			h.cookie = c
		}
	}
	if h.cookie == nil {
		h.t.Fatal("no session cookie after setup")
	}
}

func (h *harness) do(req *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func (h *harness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func (h *harness) post(path string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return h.do(req)
}

// redirect asserts a 303 and returns the decoded flash.
func (h *harness) redirect(rec *httptest.ResponseRecorder) (msg, errMsg string) {
	h.t.Helper()
	if rec.Code != http.StatusSeeOther {
		h.t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	return u.Query().Get("msg"), u.Query().Get("err")
}

func (h *harness) page(path string) string {
	h.t.Helper()
	rec := h.get(path)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET %s: code = %d body = %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestUnauthenticatedGetRedirectsToLogin(t *testing.T) {
	h := newHarness(t)
	h.cookie = nil
	rec := h.get("/apps/demo")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("code = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.post("/apps/demo/apply", url.Values{"confirm": {"demo"}}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("an unauthenticated POST must bounce: code = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if h.cloud.Asked("env-vars") {
		t.Fatal("an unauthenticated request reached the provider")
	}
}

func TestDashboardRendersTheConfigsApps(t *testing.T) {
	h := newHarness(t)
	body := h.page("/")
	for _, want := range []string{`href="/apps/demo"`, `href="/apps/other"`, "not planned in this session", "upkeep.yaml", ">demo<"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in dashboard:\n%s", want, body)
		}
	}
	// The sidebar owner is the first app.
	if !strings.Contains(body, "<b>demo</b>") {
		t.Fatal("the owner should be the config's first app")
	}
}

func TestPlanShowsTheFakesDrift(t *testing.T) {
	h := newHarness(t)
	msg, errMsg := h.redirect(h.post("/apps/demo/plan", nil))
	if errMsg != "" || !strings.Contains(msg, "planned demo") {
		t.Fatalf("msg = %q err = %q", msg, errMsg)
	}
	body := h.page("/apps/demo")
	for _, want := range []string{
		`chip on">CREATE`, "R2_BUCKET", "not set on the service",
		`chip violet">MANUAL`, "FEE_ACCOUNT_INFO", "real bank details",
		"r2-bucket", "demo-bucket",
		"1 of these cannot be automated",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in app page:\n%s", want, body)
		}
	}
	if strings.Contains(body, ">APP_ENV<") {
		t.Fatal("a matching variable is not a change")
	}
	// The dashboard's stat cards count the run.
	dash := h.page("/")
	if !strings.Contains(dash, "1 planned in this session") || !strings.Contains(dash, `chip violet">1 MANUAL`) {
		t.Fatalf("dashboard should summarise the plan:\n%s", dash)
	}
	// And the activity log has it.
	if !strings.Contains(h.page("/activity"), "planned: ") {
		t.Fatal("the plan should be in the activity log")
	}
}

func TestApplyNeedsTheAppNameTyped(t *testing.T) {
	h := newHarness(t)
	_, errMsg := h.redirect(h.post("/apps/demo/apply", url.Values{"confirm": {"dmeo"}}))
	if !strings.Contains(errMsg, "type the app name") {
		t.Fatalf("err = %q", errMsg)
	}
	if h.cloud.Env["srv-000000000000000000"]["R2_BUCKET"] != "" || h.cloud.Buckets["demo-bucket"] {
		t.Fatal("a refused apply wrote to the provider")
	}
	if !strings.Contains(h.page("/activity"), "apply refused") {
		t.Fatal("the refusal should be in the activity log")
	}
}

func TestApplyConvergesTheFake(t *testing.T) {
	h := newHarness(t)
	msg, errMsg := h.redirect(h.post("/apps/demo/apply", url.Values{"confirm": {"demo"}}))
	if errMsg != "" || !strings.Contains(msg, "verified") {
		t.Fatalf("msg = %q err = %q", msg, errMsg)
	}
	if h.cloud.Env["srv-000000000000000000"]["R2_BUCKET"] != "demo-bucket" {
		t.Fatalf("the value never reached the service: %v", h.cloud.Env)
	}
	if !h.cloud.Buckets["demo-bucket"] {
		t.Fatal("the bucket was not created")
	}
	body := h.page("/apps/demo")
	for _, want := range []string{"verified: re-read the live state", "re-read after the apply", `chip violet">MANUAL`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q after apply:\n%s", want, body)
		}
	}
	if strings.Contains(body, `chip on">CREATE`) {
		t.Fatal("after a converged apply nothing is left to create")
	}
}

// The failure the verification exists for, through the page.
func TestApplySaysWhenAWriteDidNotTake(t *testing.T) {
	h := newHarness(t)
	h.cloud.DeafWrites = true
	_, errMsg := h.redirect(h.post("/apps/demo/apply", url.Values{"confirm": {"demo"}}))
	if !strings.Contains(errMsg, "still not in place") {
		t.Fatalf("err = %q", errMsg)
	}
	body := h.page("/apps/demo")
	if !strings.Contains(body, "APPLIED BUT DID NOT TAKE") || !strings.Contains(body, `chip bad">apply failed`) {
		t.Fatalf("the reader should be told plainly:\n%s", body)
	}
}

func TestPlanAllCoversEveryApp(t *testing.T) {
	h := newHarness(t)
	msg, errMsg := h.redirect(h.post("/plan", nil))
	if errMsg != "" || !strings.Contains(msg, "planned 2 app(s)") {
		t.Fatalf("msg = %q err = %q", msg, errMsg)
	}
	body := h.page("/apps")
	if !strings.Contains(body, "matches the config") || !strings.Contains(body, `chip on">2 CREATE`) {
		t.Fatalf("apps page should show both outcomes:\n%s", body)
	}
}

func TestSettingsChangesThePassword(t *testing.T) {
	h := newHarness(t)
	body := h.page("/settings")
	for _, want := range []string{"Render", `chip on">present`, "Neon", `chip bad">missing`, "auth.json", "activity.jsonl", "upkeep.yaml"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in settings:\n%s", want, body)
		}
	}
	_, errMsg := h.redirect(h.post("/settings/password", url.Values{"current": {"wrong"}, "password": {"new password 1"}, "confirm": {"new password 1"}}))
	if !strings.Contains(errMsg, "current password is wrong") {
		t.Fatalf("err = %q", errMsg)
	}
	msg, errMsg := h.redirect(h.post("/settings/password", url.Values{"current": {"correct horse"}, "password": {"new password 1"}, "confirm": {"new password 1"}}))
	if errMsg != "" || !strings.Contains(msg, "password changed") {
		t.Fatalf("msg = %q err = %q", msg, errMsg)
	}
	// The new password signs in; the old one does not.
	h.cookie = nil
	if rec := h.post("/login", url.Values{"password": {"correct horse"}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not the password") {
		t.Fatalf("old password accepted: code = %d", rec.Code)
	}
	if rec := h.post("/login", url.Values{"password": {"new password 1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("new password refused: code = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownAppIs404(t *testing.T) {
	h := newHarness(t)
	if rec := h.get("/apps/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
	if rec := h.post("/apps/nope/plan", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAppPageReadsLiveStatus(t *testing.T) {
	h := newHarness(t)
	body := h.page("/apps/demo")
	for _, want := range []string{"Live status", "srv-000000000000000000", "not set: FEE_ACCOUNT_INFO R2_BUCKET", "private, 0 CORS rule(s)", `name="deploy"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in app page:\n%s", want, body)
		}
	}
}
