// Package workers plans a Cloudflare Worker's secrets and routes.
//
// For a product whose backend is a Worker rather than a container. It manages
// the two things that break quietly:
//
//   - A secret the script expects and the account does not have. The Worker
//     deploys, serves most requests, and one code path throws — which is the
//     failure this whole tool was written after.
//   - A route the Worker should answer on and does not. Traffic reaches the
//     zone's normal handling instead, so the site "works" and one path is
//     someone else's.
//
// Like Fly, Cloudflare returns a secret's NAME and never its value. So upkeep
// reports a secret as missing and can set one, but cannot tell you an existing
// one is wrong, and it says nothing rather than claiming a comparison it did
// not make.
//
// The script itself is not managed. Uploading a Worker is `wrangler deploy`,
// which does bundling, source maps and migrations — reimplementing it to own
// one more field would be a worse tool, not a more complete one.
package workers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/secret"
)

type API interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

var _ API = (*cfapi.Client)(nil)

type binding struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type route struct {
	ID      string `json:"id,omitempty"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

// Plan diffs the declared secrets and routes against the account.
func Plan(
	ctx context.Context,
	api API,
	w config.Workers,
	getenv func(string) string,
	secrets *secret.Set,
) (plan.Plan, error) {
	var out plan.Plan

	if err := planSecrets(ctx, api, w, getenv, secrets, &out); err != nil {
		return out, err
	}
	if err := planRoutes(ctx, api, w, &out); err != nil {
		return out, err
	}
	return out, nil
}

func planSecrets(
	ctx context.Context,
	api API,
	w config.Workers,
	getenv func(string) string,
	secrets *secret.Set,
	out *plan.Plan,
) error {
	if len(w.Secrets) == 0 {
		return nil
	}
	base := fmt.Sprintf("/accounts/%s/workers/scripts/%s", w.AccountID, w.Script)

	var live []binding
	if err := api.Do(ctx, "GET", base+"/secrets", nil, &live); err != nil {
		if !cfapi.NotConfigured(err) {
			return fmt.Errorf("read %s secrets: %w", w.Script, err)
		}
		// A script with no secrets yet answers as absent, which is the normal
		// state of a new Worker rather than a failure.
	}
	have := map[string]bool{}
	for _, b := range live {
		have[b.Name] = true
	}

	declared := make([]config.EnvVar, len(w.Secrets))
	copy(declared, w.Secrets)
	sort.Slice(declared, func(i, j int) bool { return declared[i].Key < declared[j].Key })

	for _, want := range declared {
		if want.Manual {
			if !have[want.Key] {
				out.Add(plan.Action{
					Op: plan.OpManual, Resource: "worker-secret", Target: want.Key,
					Detail: want.Why,
				})
			}
			continue
		}

		value, problem := resolve(ctx, want, getenv, secrets)
		if problem != "" {
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "worker-secret", Target: want.Key,
				Detail: problem,
			})
			continue
		}
		if have[want.Key] {
			// Cloudflare returns names, not values. Silence is the honest
			// answer: existence was checked and nothing else.
			continue
		}
		out.Add(plan.Action{
			Op: plan.OpCreate, Resource: "worker-secret", Target: want.Key,
			Detail: describe(want),
			Do: func(ctx context.Context) error {
				return api.Do(ctx, "PUT", base+"/secrets", map[string]string{
					"name": want.Key, "text": value, "type": "secret_text",
				}, nil)
			},
		})
	}
	return nil
}

func planRoutes(ctx context.Context, api API, w config.Workers, out *plan.Plan) error {
	if len(w.Routes) == 0 {
		return nil
	}
	if w.ZoneID == "" {
		out.Add(plan.Action{
			Op: plan.OpManual, Resource: "worker-route", Target: w.Script,
			Detail: "routes are declared but workers.zoneId is not set, so upkeep cannot check them",
		})
		return nil
	}
	base := fmt.Sprintf("/zones/%s/workers/routes", w.ZoneID)

	var live []route
	if err := api.Do(ctx, "GET", base, nil, &live); err != nil {
		return fmt.Errorf("read routes for zone %s: %w", w.ZoneID, err)
	}
	have := map[string]route{}
	for _, r := range live {
		have[r.Pattern] = r
	}

	declared := append([]string(nil), w.Routes...)
	sort.Strings(declared)

	for _, pattern := range declared {
		current, exists := have[pattern]
		switch {
		case !exists:
			out.Add(plan.Action{
				Op: plan.OpCreate, Resource: "worker-route", Target: pattern,
				Detail: "no route, so this path is served by the zone's normal handling instead",
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "POST", base,
						route{Pattern: pattern, Script: w.Script}, nil)
				},
			})
		case current.Script != w.Script:
			// The most confusing failure of the three: the route exists, so
			// nothing looks missing, and another script is answering.
			id := current.ID
			out.Add(plan.Action{
				Op: plan.OpUpdate, Resource: "worker-route", Target: pattern,
				Detail: fmt.Sprintf("answered by %q, not %q", current.Script, w.Script),
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "PUT", base+"/"+id,
						route{Pattern: pattern, Script: w.Script}, nil)
				},
			})
		}
	}

	// Routes upkeep did not declare are reported, never removed: a live route
	// has traffic behind it.
	var extra []string
	declaredSet := map[string]bool{}
	for _, p := range declared {
		declaredSet[p] = true
	}
	for _, r := range live {
		if !declaredSet[r.Pattern] && r.Script == w.Script {
			extra = append(extra, r.Pattern)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		out.Add(plan.Action{
			Op: plan.OpManual, Resource: "worker-route", Target: w.Script,
			Detail: fmt.Sprintf("also answers %s, which the config does not list — left alone",
				strings.Join(extra, ", ")),
		})
	}
	return nil
}

func resolve(
	ctx context.Context,
	want config.EnvVar,
	getenv func(string) string,
	secrets *secret.Set,
) (value, problem string) {
	switch {
	case want.ValueFrom != "":
		resolved, err := secrets.Resolve(ctx, want.ValueFrom)
		if err != nil {
			return "", err.Error()
		}
		return resolved, ""
	case want.ValueEnv != "":
		if v := getenv(want.ValueEnv); v != "" {
			return v, ""
		}
		return "", fmt.Sprintf("set $%s locally, then re-run", want.ValueEnv)
	}
	return want.Value, ""
}

func describe(want config.EnvVar) string {
	const missing = "not set on the script"
	switch {
	case want.ValueFrom != "":
		return fmt.Sprintf("%s (value from %s, not shown)", missing, want.ValueFrom)
	case want.ValueEnv != "":
		return fmt.Sprintf("%s (value from $%s, not shown)", missing, want.ValueEnv)
	}
	return missing + " → " + want.Value
}
