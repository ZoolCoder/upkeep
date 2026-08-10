package workers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/secret"
)

type fakeCF struct {
	get    map[string]string
	absent map[string]bool
	wrote  map[string]any
}

func newCF() *fakeCF {
	return &fakeCF{get: map[string]string{}, absent: map[string]bool{}, wrote: map[string]any{}}
}

func (f *fakeCF) Do(_ context.Context, method, path string, body, result any) error {
	if method != "GET" {
		f.wrote[method+" "+path] = body
		return nil
	}
	if f.absent[path] {
		return &cfapi.Error{Status: 404, Code: 10007, Message: "not found", Endpoint: path}
	}
	raw, ok := f.get[path]
	if !ok || result == nil {
		return nil
	}
	return json.Unmarshal([]byte(raw), result)
}

const (
	acct    = "acct"
	zone    = "z1"
	secrets = "/accounts/acct/workers/scripts/api/secrets"
	routes  = "/zones/z1/workers/routes"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func planFor(t *testing.T, api API, w config.Workers, getenv func(string) string) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, w, getenv, secret.Default())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(p plan.Plan, target string) (plan.Action, bool) {
	for _, a := range p.Actions {
		if a.Target == target {
			return a, true
		}
	}
	return plan.Action{}, false
}

// The failure this whole tool was written after, in Worker form: the script
// deploys, serves most requests, and one code path throws.
func TestAMissingSecretIsPlanned(t *testing.T) {
	api := newCF()
	api.get[secrets] = `[{"name":"EXISTING","type":"secret_text"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api",
		Secrets: []config.EnvVar{
			{Key: "EXISTING", Value: "x"},
			{Key: "MISSING", Value: "y"},
		},
	}, env())

	a, ok := find(p, "MISSING")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("expected a CREATE, got %+v", p.Actions)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.wrote["PUT "+secrets] == nil {
		t.Error("apply did not set the secret")
	}
}

// Cloudflare returns names, not values. Silence is the honest answer.
func TestAnExistingSecretIsNotClaimedToMatch(t *testing.T) {
	api := newCF()
	api.get[secrets] = `[{"name":"TOKEN","type":"secret_text"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api",
		Secrets: []config.EnvVar{{Key: "TOKEN", ValueEnv: "TOKEN"}},
	}, env("TOKEN", "something-entirely-different"))

	if !p.Empty() {
		t.Fatalf("the value cannot be seen, so nothing should be claimed: %+v", p.Actions)
	}
}

// A new Worker has no secrets endpoint yet; that is its normal state.
func TestAScriptWithNoSecretsYetIsNotAFailure(t *testing.T) {
	api := newCF()
	api.absent[secrets] = true

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api",
		Secrets: []config.EnvVar{{Key: "TOKEN", Value: "x"}},
	}, env())

	a, ok := find(p, "TOKEN")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("expected the secret to be planned, got %+v", p.Actions)
	}
}

func TestNoSecretValueReachesThePlan(t *testing.T) {
	const value = "a-real-looking-secret"
	api := newCF()
	api.get[secrets] = `[]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api",
		Secrets: []config.EnvVar{{Key: "TOKEN", ValueEnv: "TOKEN"}},
	}, env("TOKEN", value))

	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), value) {
		t.Fatal("the plan printed the value")
	}
}

// A missing route is silent: traffic reaches the zone's normal handling, so
// the site "works" and one path is someone else's.
func TestAMissingRouteIsPlanned(t *testing.T) {
	api := newCF()
	api.get[routes] = `[{"id":"r1","pattern":"example.com/api/*","script":"api"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api", ZoneID: zone,
		Routes: []string{"example.com/api/*", "example.com/hooks/*"},
	}, env())

	a, ok := find(p, "example.com/hooks/*")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("expected a CREATE, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "zone's normal handling") {
		t.Errorf("the detail should say what happens instead, got %q", a.Detail)
	}
}

// The most confusing failure of the three: the route exists, so nothing looks
// missing, and another script is answering.
func TestARouteAnsweredByAnotherScript(t *testing.T) {
	api := newCF()
	api.get[routes] = `[{"id":"r1","pattern":"example.com/api/*","script":"old-api"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api", ZoneID: zone,
		Routes: []string{"example.com/api/*"},
	}, env())

	a, ok := find(p, "example.com/api/*")
	if !ok || a.Op != plan.OpUpdate {
		t.Fatalf("expected an UPDATE, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "old-api") {
		t.Errorf("it should name who is answering, got %q", a.Detail)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.wrote["PUT "+routes+"/r1"] == nil {
		t.Errorf("it should update that route by id, wrote %v", api.wrote)
	}
}

// A live route has traffic behind it.
func TestUndeclaredRoutesAreReportedNeverRemoved(t *testing.T) {
	api := newCF()
	api.get[routes] = `[
		{"id":"r1","pattern":"example.com/api/*","script":"api"},
		{"id":"r2","pattern":"example.com/legacy/*","script":"api"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api", ZoneID: zone,
		Routes: []string{"example.com/api/*"},
	}, env())

	a, ok := find(p, "api")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected a MANUAL report, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "legacy") {
		t.Errorf("it should name the route, got %q", a.Detail)
	}
	for _, action := range p.Actions {
		if action.Op == plan.OpDelete {
			t.Fatal("upkeep must never plan a route deletion")
		}
	}
}

// Another script's routes on the same zone are not this app's business.
func TestRoutesBelongingToAnotherScriptAreIgnored(t *testing.T) {
	api := newCF()
	api.get[routes] = `[
		{"id":"r1","pattern":"example.com/api/*","script":"api"},
		{"id":"r2","pattern":"example.com/site/*","script":"a-different-worker"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api", ZoneID: zone,
		Routes: []string{"example.com/api/*"},
	}, env())

	if !p.Empty() {
		t.Fatalf("only this script's routes are this app's concern: %+v", p.Actions)
	}
}

func TestEverythingMatchingPlansNothing(t *testing.T) {
	api := newCF()
	api.get[secrets] = `[{"name":"TOKEN","type":"secret_text"}]`
	api.get[routes] = `[{"id":"r1","pattern":"example.com/api/*","script":"api"}]`

	p := planFor(t, api, config.Workers{
		AccountID: acct, Script: "api", ZoneID: zone,
		Secrets: []config.EnvVar{{Key: "TOKEN", Value: "x"}},
		Routes:  []string{"example.com/api/*"},
	}, env())

	if !p.Empty() {
		t.Fatalf("expected nothing, got %+v", p.Actions)
	}
}
