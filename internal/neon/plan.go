// Package neon reports on the database behind an app.
//
// It plans no writes. What it checks is that the branch the config names still
// exists, and that the service's DATABASE_URL is the one the operator thinks it
// is — a service quietly pointing at a deleted branch, or at last month's
// project, is the failure this catches.
package neon

import (
	"context"
	"fmt"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/neonapi"
	"github.com/zoolcoder/upkeep/internal/plan"
)

// API is the read half, narrowed for tests.
type API interface {
	Branches(ctx context.Context, projectID string) ([]neonapi.Branch, error)
}

var _ API = (*neonapi.Client)(nil)

// Plan checks the declared branch exists. liveDatabaseURL is the service's
// current value; it is compared and discarded, never rendered.
func Plan(ctx context.Context, api API, n config.Neon, liveDatabaseURL string, getenv func(string) string) (plan.Plan, error) {
	var out plan.Plan

	if n.Branch != "" {
		branches, err := api.Branches(ctx, n.ProjectID)
		if err != nil {
			return out, fmt.Errorf("read neon project %s: %w", n.ProjectID, err)
		}
		found := false
		for _, b := range branches {
			if b.Name == n.Branch {
				found = true
				break
			}
		}
		if !found {
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "neon-branch", Target: n.Branch,
				Detail: fmt.Sprintf("project %s has no branch by that name; upkeep does not create or delete branches", n.ProjectID),
			})
		}
	}

	if n.DatabaseURLEnv != "" {
		want := getenv(n.DatabaseURLEnv)
		switch {
		case want == "":
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "neon-url", Target: "DATABASE_URL",
				Detail: fmt.Sprintf("set $%s locally to check the service points at this project", n.DatabaseURLEnv),
			})
		case liveDatabaseURL == "":
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "neon-url", Target: "DATABASE_URL",
				Detail: "the service has no DATABASE_URL; set it from the Neon connection string",
			})
		case liveDatabaseURL != want:
			// Neither value is printed. Which one is right is a judgement about
			// live data, and this tool does not make it.
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "neon-url", Target: "DATABASE_URL",
				Detail: fmt.Sprintf("the service's value differs from $%s; upkeep will not repoint a running service at another database", n.DatabaseURLEnv),
			})
		}
	}
	return out, nil
}
