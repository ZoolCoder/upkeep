package pages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

type fakeCF struct {
	project string
	missing bool
	wrote   map[string]any
}

func (f *fakeCF) Do(_ context.Context, method, path string, body, result any) error {
	if method != "GET" {
		if f.wrote == nil {
			f.wrote = map[string]any{}
		}
		f.wrote[path] = body
		return nil
	}
	if f.missing {
		return &cfapi.Error{Status: 404, Code: 8000007, Message: "Project not found.", Endpoint: path}
	}
	return json.Unmarshal([]byte(f.project), result)
}

func planFor(t *testing.T, api API, p config.Pages) plan.Plan {
	t.Helper()
	out, err := Plan(context.Background(), api, p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var live = `{"name":"site","production_branch":"main",
  "domains":["site.pages.dev","www.example.com"]}`

func TestAMatchingProjectPlansNothing(t *testing.T) {
	p := planFor(t, &fakeCF{project: live}, config.Pages{
		AccountID: "a", Project: "site", ProductionBranch: "main",
		Domains: []string{"site.pages.dev", "www.example.com"},
	})
	if !p.Empty() {
		t.Fatalf("expected no changes, got %+v", p.Actions)
	}
}

// A preview deployment is a different origin, and an exact CORS or allowlist
// match refuses it — so the production branch is not cosmetic.
func TestAWrongProductionBranchIsPlanned(t *testing.T) {
	api := &fakeCF{project: live}
	p := planFor(t, api, config.Pages{
		AccountID: "a", Project: "site", ProductionBranch: "release",
	})
	if len(p.Actions) != 1 || p.Actions[0].Op != plan.OpUpdate {
		t.Fatalf("expected one UPDATE, got %+v", p.Actions)
	}
	if !strings.Contains(p.Actions[0].Detail, "different origin") {
		t.Errorf("the detail should say why it matters, got %q", p.Actions[0].Detail)
	}
	if err := p.Actions[0].Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.wrote["/accounts/a/pages/projects/site"] == nil {
		t.Error("apply did not patch the project")
	}
}

// A hostname with traffic on it is not a typo to act on.
func TestAnUnknownDomainIsReportedNotRemoved(t *testing.T) {
	p := planFor(t, &fakeCF{project: live}, config.Pages{
		AccountID: "a", Project: "site", ProductionBranch: "main",
		Domains: []string{"site.pages.dev"},
	})
	if len(p.Actions) != 1 || p.Actions[0].Op != plan.OpManual {
		t.Fatalf("expected one MANUAL, got %+v", p.Actions)
	}
	if p.Actions[0].Do != nil {
		t.Error("a manual action must not be executable")
	}
	if !strings.Contains(p.Actions[0].Detail, "www.example.com") {
		t.Errorf("it should name the extra domain, got %q", p.Actions[0].Detail)
	}
}

func TestADeclaredDomainThatIsNotAttachedIsManual(t *testing.T) {
	p := planFor(t, &fakeCF{project: live}, config.Pages{
		AccountID: "a", Project: "site", ProductionBranch: "main",
		Domains: []string{"site.pages.dev", "www.example.com", "new.example.com"},
	})
	found := false
	for _, a := range p.Actions {
		if a.Target == "new.example.com" && a.Op == plan.OpManual {
			found = true
			if !strings.Contains(a.Detail, "DNS") {
				t.Errorf("it should say what else is needed, got %q", a.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected the new domain to be reported, got %+v", p.Actions)
	}
}

// Creating a Pages project needs a build config upkeep does not model, and the
// first `wrangler pages deploy` makes one anyway.
func TestAnAbsentProjectIsManual(t *testing.T) {
	p := planFor(t, &fakeCF{missing: true}, config.Pages{AccountID: "a", Project: "site"})
	if len(p.Actions) != 1 || p.Actions[0].Op != plan.OpManual {
		t.Fatalf("expected one MANUAL, got %+v", p.Actions)
	}
	if !strings.Contains(p.Actions[0].Detail, "wrangler pages deploy") {
		t.Errorf("it should say how to make one, got %q", p.Actions[0].Detail)
	}
}
