package renderenv

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/secret"
)

type fakeService struct {
	live map[string]string
	set  map[string]string
	err  error
}

func newFake(live map[string]string) *fakeService {
	return &fakeService{live: live, set: map[string]string{}}
}

func (f *fakeService) EnvVars(context.Context, string) (map[string]string, error) {
	return f.live, f.err
}

func (f *fakeService) SetEnvVar(_ context.Context, _, key, value string) error {
	f.set[key] = value
	return nil
}

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func planFor(t *testing.T, api Setter, r config.Render, getenv func(string) string) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, r, getenv, secret.Default())
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

// The failure this whole tool exists for: a service missing variables it needs,
// which is an error nowhere. zoolaqar served for days with no R2_* set — photo
// upload answered 404 and nothing anywhere said so.
func TestAMissingVariableIsPlanned(t *testing.T) {
	api := newFake(map[string]string{"APP_ENV": "production"})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env: []config.EnvVar{
			{Key: "APP_ENV", Value: "production"},
			{Key: "R2_BUCKET", Value: "zoolaqar"},
		},
	}, env())

	if len(p.Actions) != 1 {
		t.Fatalf("expected one change, got %d", len(p.Actions))
	}
	a, ok := find(p, "R2_BUCKET")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("R2_BUCKET should be a CREATE, got %+v", a)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.set["R2_BUCKET"] != "zoolaqar" {
		t.Errorf("applied %q", api.set["R2_BUCKET"])
	}
}

func TestAMatchingEnvironmentPlansNothing(t *testing.T) {
	api := newFake(map[string]string{"APP_ENV": "production", "TZ": "UTC"})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env: []config.EnvVar{
			{Key: "APP_ENV", Value: "production"},
			{Key: "TZ", Value: "UTC"},
		},
	}, env())
	if !p.Empty() {
		t.Fatalf("expected no changes, got %+v", p.Actions)
	}
}

// Render carries variables its own platform set. Reconciling only what the
// config names means the tool can never delete one by omission.
func TestVariablesTheConfigDoesNotNameAreLeftAlone(t *testing.T) {
	api := newFake(map[string]string{"RENDER_INTERNAL": "x", "APP_ENV": "production"})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env:       []config.EnvVar{{Key: "APP_ENV", Value: "production"}},
	}, env())
	if !p.Empty() {
		t.Fatalf("expected no changes, got %+v", p.Actions)
	}
}

// A secret's value must not appear in the plan, which is printed to a terminal
// and pasted into chats and tickets.
func TestASecretsValueIsNeverInThePlan(t *testing.T) {
	const secret = "super-secret-access-key"
	api := newFake(map[string]string{})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env:       []config.EnvVar{{Key: "R2_SECRET_ACCESS_KEY", ValueEnv: "R2_SECRET"}},
	}, env("R2_SECRET", secret))

	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), secret) {
		t.Fatal("the plan printed the secret")
	}
	if !strings.Contains(rendered.String(), "$R2_SECRET") {
		t.Errorf("the plan should name the source variable, got %q", rendered.String())
	}

	// It still applies the real value.
	a, _ := find(p, "R2_SECRET_ACCESS_KEY")
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.set["R2_SECRET_ACCESS_KEY"] != secret {
		t.Error("the secret did not reach the service")
	}
}

// An unset source is not something to guess at, and not something to skip.
func TestAnUnsetSourceBecomesAManualAction(t *testing.T) {
	api := newFake(map[string]string{})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env:       []config.EnvVar{{Key: "R2_SECRET_ACCESS_KEY", ValueEnv: "R2_SECRET"}},
	}, env())

	a, ok := find(p, "R2_SECRET_ACCESS_KEY")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected a MANUAL action, got %+v", a)
	}
	if a.Do != nil {
		t.Error("a manual action must not be executable")
	}
	if !strings.Contains(a.Detail, "$R2_SECRET") {
		t.Errorf("it should say which variable to set, got %q", a.Detail)
	}
}

// FEE_ACCOUNT_INFO is real bank details shown to seekers verbatim. There is no
// value a tool could put there.
func TestAManualValueIsReportedNotInvented(t *testing.T) {
	api := newFake(map[string]string{})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env: []config.EnvVar{{
			Key: "FEE_ACCOUNT_INFO", Manual: true,
			Why: "real bank details, shown to seekers verbatim",
		}},
	}, env())

	a, ok := find(p, "FEE_ACCOUNT_INFO")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected MANUAL, got %+v", a)
	}
	if !strings.Contains(a.Detail, "bank details") {
		t.Errorf("the reason should reach the reader, got %q", a.Detail)
	}
}

// Once it is set, it stops being an outstanding action — otherwise every run
// forever reports the same thing and readers learn to ignore the section.
func TestAManualValueAlreadySetIsNotReported(t *testing.T) {
	api := newFake(map[string]string{"FEE_ACCOUNT_INFO": "بنك الخرطوم — حساب ١٢٣"})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env: []config.EnvVar{{
			Key: "FEE_ACCOUNT_INFO", Manual: true, Why: "real bank details",
		}},
	}, env())
	if !p.Empty() {
		t.Fatalf("expected nothing outstanding, got %+v", p.Actions)
	}
}

func TestAChangedValueIsAnUpdate(t *testing.T) {
	api := newFake(map[string]string{"R2_BUCKET": "old"})
	p := planFor(t, api, config.Render{
		ServiceID: "srv-1",
		Env:       []config.EnvVar{{Key: "R2_BUCKET", Value: "zoolaqar"}},
	}, env())

	a, ok := find(p, "R2_BUCKET")
	if !ok || a.Op != plan.OpUpdate {
		t.Fatalf("expected UPDATE, got %+v", a)
	}
}

// A team's values live in a password manager, not in anyone's shell.
func TestAValueFromASecretManagerIsResolvedAndNeverPrinted(t *testing.T) {
	const value = "vault-held-credential"
	api := newFake(map[string]string{})
	resolver := secret.NewSet(fakeVault{value: value})

	p, err := Plan(context.Background(), api, config.Render{
		ServiceID: "srv-1",
		Env:       []config.EnvVar{{Key: "RESEND_API_KEY", ValueFrom: "test://team/resend"}},
	}, env(), resolver)
	if err != nil {
		t.Fatal(err)
	}

	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.String(), value) {
		t.Fatal("the plan printed the value")
	}
	if !strings.Contains(rendered.String(), "test://team/resend") {
		t.Errorf("it should name the reference, got %q", rendered.String())
	}

	a, _ := find(p, "RESEND_API_KEY")
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.set["RESEND_API_KEY"] != value {
		t.Error("the resolved value did not reach the service")
	}
}

// One unreachable vault entry must not stop the rest of a plan, and the reason
// is what the reader needs.
func TestAnUnreachableReferenceBecomesAManualAction(t *testing.T) {
	p, err := Plan(context.Background(), newFake(map[string]string{}), config.Render{
		ServiceID: "srv-1",
		Env: []config.EnvVar{
			{Key: "RESEND_API_KEY", ValueFrom: "test://team/missing"},
			{Key: "R2_BUCKET", Value: "still-planned"},
		},
	}, env(), secret.NewSet(fakeVault{err: true}))
	if err != nil {
		t.Fatal(err)
	}

	a, ok := find(p, "RESEND_API_KEY")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected MANUAL, got %+v", a)
	}
	if other, ok := find(p, "R2_BUCKET"); !ok || other.Op != plan.OpCreate {
		t.Error("one unreachable value should not stop the rest of the plan")
	}
}

type fakeVault struct {
	value string
	err   bool
}

func (fakeVault) Scheme() string { return "test" }

func (f fakeVault) Resolve(context.Context, string) (string, error) {
	if f.err {
		return "", errAgain
	}
	return f.value, nil
}

var errAgain = errors.New("vault is locked")
