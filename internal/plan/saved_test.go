package plan

import (
	"strings"
	"testing"
)

func twoActions() Plan {
	return Plan{Actions: []Action{
		{Op: OpCreate, Resource: "render-env", Target: "R2_BUCKET", Detail: "not set on the service → b", Do: noop},
		{Op: OpUpdate, Resource: "r2-cors", Target: "b", Detail: "differs from the config", Do: noop},
	}}
}

func roundTrip(t *testing.T, p Plan) Saved {
	t.Helper()
	var buf strings.Builder
	if err := p.Save(&buf); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSaved(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestASavedPlanMatchesTheOneItCameFrom(t *testing.T) {
	p := twoActions()
	if err := roundTrip(t, p).Matches(p); err != nil {
		t.Fatalf("a plan should match itself: %v", err)
	}
}

// The point of saving: apply exactly what was reviewed. If the world moved,
// say so rather than quietly doing something nobody saw.
func TestAChangedWorldIsRefused(t *testing.T) {
	saved := roundTrip(t, twoActions())

	fresh := twoActions()
	fresh.Actions[0].Detail = "differs from the config → b"

	err := saved.Matches(fresh)
	if err == nil {
		t.Fatal("a different reason for the same key is a different change")
	}
	if !strings.Contains(err.Error(), "re-run plan") {
		t.Errorf("the error should say what to do, got %v", err)
	}
	if !strings.Contains(err.Error(), "reviewed:") {
		t.Errorf("it should show both, got %v", err)
	}
}

func TestAnExtraChangeIsRefused(t *testing.T) {
	saved := roundTrip(t, twoActions())

	fresh := twoActions()
	fresh.Actions = append(fresh.Actions, Action{
		Op: OpCreate, Resource: "render-env", Target: "NEW", Detail: "not set", Do: noop,
	})
	err := saved.Matches(fresh)
	if err == nil {
		t.Fatal("an action nobody reviewed must not be applied")
	}
	if !strings.Contains(err.Error(), "2 action(s), there are now 3") {
		t.Errorf("the error should count both, got %v", err)
	}
}

// Order must not matter to a reviewer, so it must not matter here either: both
// sides are sorted the same way before comparison.
func TestOrderOfTheSameChangesStillMatches(t *testing.T) {
	saved := roundTrip(t, twoActions())

	fresh := twoActions()
	fresh.Actions[0], fresh.Actions[1] = fresh.Actions[1], fresh.Actions[0]

	if err := saved.Matches(fresh); err != nil {
		t.Fatalf("the same changes in another order are the same plan: %v", err)
	}
}

// A saved plan is written to disk, so the redaction rule has to hold there too.
func TestASavedPlanCarriesNoValues(t *testing.T) {
	p := Plan{Actions: []Action{{
		Op: OpUpdate, Resource: "render-env", Target: "R2_SECRET_ACCESS_KEY",
		Detail: "differs from the config (value from $R2_SECRET, not shown)", Do: noop,
	}}}
	var buf strings.Builder
	if err := p.Save(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "hunter2") {
		t.Error("a value reached the file")
	}
	if !strings.Contains(buf.String(), "$R2_SECRET") {
		t.Errorf("it should still name the source, got %s", buf.String())
	}
}

func TestAPlanFromAnotherVersionIsRefused(t *testing.T) {
	_, err := LoadSaved(strings.NewReader(`{"version":99,"actions":[]}`))
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("got %v", err)
	}
}

// Reviewing a file and reviewing a run must look identical, or people stop
// reading one of them.
func TestASavedPlanRendersLikeALivePlan(t *testing.T) {
	p := twoActions()
	var live, fromFile strings.Builder
	if err := p.Write(&live); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(t, p).Write(&fromFile); err != nil {
		t.Fatal(err)
	}
	if live.String() != fromFile.String() {
		t.Errorf("renderings differ:\nlive:\n%s\nfile:\n%s", live.String(), fromFile.String())
	}
}
