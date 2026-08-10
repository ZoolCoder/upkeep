package neon

import (
	"context"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/neonapi"
	"github.com/zoolcoder/upkeep/internal/plan"
)

type fakeNeon struct {
	branches []string
	err      error
}

func (f fakeNeon) Branches(context.Context, string) ([]neonapi.Branch, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []neonapi.Branch
	for _, b := range f.branches {
		out = append(out, neonapi.Branch{ID: "br-" + b, Name: b})
	}
	return out, nil
}

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func planFor(t *testing.T, api API, n config.Neon, live string, getenv func(string) string) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, n, live, getenv)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnExistingBranchPlansNothing(t *testing.T) {
	p := planFor(t, fakeNeon{branches: []string{"main", "dev"}},
		config.Neon{ProjectID: "p1", Branch: "main"}, "", env())
	if !p.Empty() {
		t.Fatalf("expected nothing, got %+v", p.Actions)
	}
}

// A service quietly pointing at a deleted branch is the failure this catches.
func TestAMissingBranchIsReported(t *testing.T) {
	p := planFor(t, fakeNeon{branches: []string{"main"}},
		config.Neon{ProjectID: "p1", Branch: "staging"}, "", env())
	if len(p.Actions) != 1 || p.Actions[0].Op != plan.OpManual {
		t.Fatalf("expected one MANUAL, got %+v", p.Actions)
	}
	if !strings.Contains(p.Actions[0].Detail, "does not create or delete branches") {
		t.Errorf("it should say what upkeep will not do, got %q", p.Actions[0].Detail)
	}
}

// Neither connection string is printed: which one is right is a judgement about
// live data, and this tool does not make it.
func TestAMismatchedDatabaseUrlIsReportedWithoutEitherValue(t *testing.T) {
	const liveURL = "postgres://user:livepass@a/db"
	const wantURL = "postgres://user:wantpass@b/db"

	p := planFor(t, fakeNeon{branches: []string{"main"}},
		config.Neon{ProjectID: "p1", Branch: "main", DatabaseURLEnv: "DATABASE_URL"},
		liveURL, env("DATABASE_URL", wantURL))

	if len(p.Actions) != 1 || p.Actions[0].Op != plan.OpManual {
		t.Fatalf("expected one MANUAL, got %+v", p.Actions)
	}
	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"livepass", "wantpass"} {
		if strings.Contains(rendered.String(), secret) {
			t.Errorf("the plan printed a connection string: %s", rendered.String())
		}
	}
	if !strings.Contains(p.Actions[0].Detail, "will not repoint a running service") {
		t.Errorf("got %q", p.Actions[0].Detail)
	}
}

func TestAMatchingDatabaseUrlPlansNothing(t *testing.T) {
	const same = "postgres://user:p@a/db"
	p := planFor(t, fakeNeon{branches: []string{"main"}},
		config.Neon{ProjectID: "p1", Branch: "main", DatabaseURLEnv: "DATABASE_URL"},
		same, env("DATABASE_URL", same))
	if !p.Empty() {
		t.Fatalf("expected nothing, got %+v", p.Actions)
	}
}

func TestAnUnsetLocalUrlSaysWhichVariable(t *testing.T) {
	p := planFor(t, fakeNeon{branches: []string{"main"}},
		config.Neon{ProjectID: "p1", Branch: "main", DatabaseURLEnv: "DATABASE_URL"},
		"postgres://a", env())
	if len(p.Actions) != 1 {
		t.Fatalf("expected one action, got %+v", p.Actions)
	}
	if !strings.Contains(p.Actions[0].Detail, "$DATABASE_URL") {
		t.Errorf("got %q", p.Actions[0].Detail)
	}
}

func TestAServiceWithNoDatabaseUrlIsReported(t *testing.T) {
	p := planFor(t, fakeNeon{branches: []string{"main"}},
		config.Neon{ProjectID: "p1", Branch: "main", DatabaseURLEnv: "DATABASE_URL"},
		"", env("DATABASE_URL", "postgres://a"))
	if len(p.Actions) != 1 || !strings.Contains(p.Actions[0].Detail, "has no DATABASE_URL") {
		t.Fatalf("got %+v", p.Actions)
	}
}
