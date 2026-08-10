package flysecrets

import (
	"context"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/flyapi"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/secret"
)

type fakeFly struct {
	live map[string]flyapi.Secret
	set  map[string]string
}

func newFly(names ...string) *fakeFly {
	live := map[string]flyapi.Secret{}
	for _, n := range names {
		live[n] = flyapi.Secret{Name: n, Digest: "sha256:whatever"}
	}
	return &fakeFly{live: live, set: map[string]string{}}
}

func (f *fakeFly) Secrets(context.Context, string) (map[string]flyapi.Secret, error) {
	return f.live, nil
}

func (f *fakeFly) SetSecret(_ context.Context, _, name, value string) error {
	f.set[name] = value
	return nil
}

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func planFor(t *testing.T, api API, f config.Fly, getenv func(string) string) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, f, getenv, secret.Default())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(p plan.Plan, key string) (plan.Action, bool) {
	for _, a := range p.Actions {
		if a.Target == key {
			return a, true
		}
	}
	return plan.Action{}, false
}

func TestAMissingSecretIsPlanned(t *testing.T) {
	api := newFly("EXISTING")
	p := planFor(t, api, config.Fly{
		App: "my-app",
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
	if api.set["MISSING"] != "y" {
		t.Errorf("the value did not reach Fly: %v", api.set)
	}
}

// The difference from Render that matters. Fly never returns a value, so an
// existing secret has been checked for EXISTENCE and nothing more — and
// reporting it as matching would claim a comparison that never happened.
func TestAnExistingSecretIsNotClaimedToMatch(t *testing.T) {
	p := planFor(t, newFly("TOKEN"), config.Fly{
		App:     "my-app",
		Secrets: []config.EnvVar{{Key: "TOKEN", ValueEnv: "TOKEN"}},
	}, env("TOKEN", "something-entirely-different"))

	if !p.Empty() {
		t.Fatalf("upkeep cannot see a Fly secret's value, so it must say nothing: %+v", p.Actions)
	}
}

// The same redaction rule as every other surface.
func TestNoValueReachesThePlan(t *testing.T) {
	const value = "a-real-looking-secret"
	p := planFor(t, newFly(), config.Fly{
		App:     "my-app",
		Secrets: []config.EnvVar{{Key: "TOKEN", ValueEnv: "TOKEN"}},
	}, env("TOKEN", value))

	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), value) {
		t.Fatal("the plan printed the value")
	}
	if !strings.Contains(rendered.String(), "$TOKEN") {
		t.Errorf("it should name the source, got %q", rendered.String())
	}
}

func TestAnUnsetSourceBecomesManual(t *testing.T) {
	p := planFor(t, newFly(), config.Fly{
		App:     "my-app",
		Secrets: []config.EnvVar{{Key: "TOKEN", ValueEnv: "TOKEN"}},
	}, env())

	a, ok := find(p, "TOKEN")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected MANUAL, got %+v", p.Actions)
	}
	if a.Do != nil {
		t.Error("a manual action must not be executable")
	}
}

func TestAManualSecretIsReportedUntilItExists(t *testing.T) {
	declared := config.Fly{
		App: "my-app",
		Secrets: []config.EnvVar{{
			Key: "STRIPE_KEY", Manual: true, Why: "minted in the Stripe dashboard",
		}},
	}

	p := planFor(t, newFly(), declared, env())
	a, ok := find(p, "STRIPE_KEY")
	if !ok || a.Op != plan.OpManual || !strings.Contains(a.Detail, "Stripe dashboard") {
		t.Fatalf("got %+v", p.Actions)
	}

	// Once it is there, it stops being outstanding — otherwise every run
	// forever reports the same thing and readers learn to ignore the section.
	if p := planFor(t, newFly("STRIPE_KEY"), declared, env()); !p.Empty() {
		t.Errorf("expected silence, got %+v", p.Actions)
	}
}

// Secrets the config does not name are left alone, like every other surface.
func TestSecretsTheConfigDoesNotNameAreUntouched(t *testing.T) {
	p := planFor(t, newFly("FLY_INTERNAL", "TOKEN"), config.Fly{
		App:     "my-app",
		Secrets: []config.EnvVar{{Key: "TOKEN", Value: "x"}},
	}, env())

	if !p.Empty() {
		t.Fatalf("expected nothing, got %+v", p.Actions)
	}
	for _, a := range p.Actions {
		if a.Op == plan.OpDelete {
			t.Fatal("upkeep must never plan a secret deletion")
		}
	}
}
