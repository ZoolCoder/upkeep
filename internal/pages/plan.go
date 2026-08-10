// Package pages plans a Cloudflare Pages project's configuration.
//
// upkeep reports domains it does not know about rather than removing them. A
// hostname is the one thing here with traffic already pointed at it, and a tool
// that prunes DNS-adjacent config on a config-file typo is a tool that takes a
// site down.
package pages

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

type API interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

var _ API = (*cfapi.Client)(nil)

type project struct {
	Name             string   `json:"name"`
	Subdomain        string   `json:"subdomain"`
	ProductionBranch string   `json:"production_branch"`
	Domains          []string `json:"domains"`
}

func Plan(ctx context.Context, api API, p config.Pages) (plan.Plan, error) {
	var out plan.Plan
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s", p.AccountID, p.Project)

	var live project
	err := api.Do(ctx, "GET", path, nil, &live)
	switch {
	case cfapi.NotConfigured(err):
		// Creating a Pages project needs a build config upkeep does not
		// model, and `wrangler pages deploy` makes one on first push anyway.
		out.Add(plan.Action{
			Op: plan.OpManual, Resource: "pages", Target: p.Project,
			Detail: "project does not exist; the first `wrangler pages deploy` creates it",
		})
		return out, nil
	case err != nil:
		return out, fmt.Errorf("read pages project %s: %w", p.Project, err)
	}

	if p.ProductionBranch != "" && live.ProductionBranch != p.ProductionBranch {
		out.Add(plan.Action{
			Op: plan.OpUpdate, Resource: "pages", Target: p.Project,
			Detail: fmt.Sprintf("production branch is %q, config says %q — a preview URL is a different origin, and an exact CORS match refuses it",
				live.ProductionBranch, p.ProductionBranch),
			Do: func(ctx context.Context) error {
				return api.Do(ctx, "PATCH", path,
					map[string]string{"production_branch": p.ProductionBranch}, nil)
			},
		})
	}

	if len(p.Domains) > 0 {
		have := map[string]bool{}
		for _, d := range live.Domains {
			have[d] = true
		}
		want := map[string]bool{}
		for _, d := range p.Domains {
			want[d] = true
			if !have[d] {
				out.Add(plan.Action{
					Op: plan.OpManual, Resource: "pages-domain", Target: d,
					Detail: fmt.Sprintf("declared for %s but not attached; adding it needs the DNS record too", p.Project),
				})
			}
		}
		var extra []string
		for _, d := range live.Domains {
			if !want[d] {
				extra = append(extra, d)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "pages-domain", Target: p.Project,
				Detail: fmt.Sprintf("serving %s, which the config does not list — left alone, since a live hostname is not a typo to act on",
					strings.Join(extra, ", ")),
			})
		}
	}
	return out, nil
}
