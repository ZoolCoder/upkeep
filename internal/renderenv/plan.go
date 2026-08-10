// Package renderenv plans a Render service's environment.
//
// This is the surface that fails silently. A variable the service needs and
// does not have is not an error anywhere — the app boots, serves most requests,
// and one feature is quietly dead. zoolaqar shipped for days with no R2_*
// variables at all: photo upload answered 404 and nothing said so.
package renderenv

import (
	"context"
	"fmt"
	"sort"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/renderapi"
	"github.com/zoolcoder/upkeep/internal/secret"
)

// Setter is the write half, so a plan can be built against a fake.
type Setter interface {
	EnvVars(ctx context.Context, serviceID string) (map[string]string, error)
	SetEnvVar(ctx context.Context, serviceID, key, value string) error
}

var _ Setter = (*renderapi.Client)(nil)

// Plan diffs the declared environment against the live one.
//
// getenv resolves valueEnv references. It is injected so tests never touch the
// process environment, and so the values it returns stay in memory: no caller
// of this package ever receives one.
func Plan(ctx context.Context, api Setter, r config.Render, getenv func(string) string, secrets *secret.Set) (plan.Plan, error) {
	var out plan.Plan

	live, err := api.EnvVars(ctx, r.ServiceID)
	if err != nil {
		return out, fmt.Errorf("read %s environment: %w", r.ServiceID, err)
	}

	declared := make([]config.EnvVar, len(r.Env))
	copy(declared, r.Env)
	sort.Slice(declared, func(i, j int) bool { return declared[i].Key < declared[j].Key })

	for _, want := range declared {
		current, present := live[want.Key]

		// A value nobody can generate. The plan says what is missing and what
		// to do about it, then leaves it alone — inventing a bank account or a
		// credential is worse than reporting neither.
		if want.Manual {
			if present && current != "" {
				continue
			}
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "render-env", Target: want.Key,
				Detail: want.Why,
			})
			continue
		}

		value := want.Value
		if want.ValueFrom != "" {
			resolved, err := secrets.Resolve(ctx, want.ValueFrom)
			if err != nil {
				// Not a crash: one unreachable vault entry should not stop the
				// rest of a plan, and the reason is what the reader needs.
				out.Add(plan.Action{
					Op: plan.OpManual, Resource: "render-env", Target: want.Key,
					Detail: err.Error(),
				})
				continue
			}
			value = resolved
		}
		if want.ValueEnv != "" {
			value = getenv(want.ValueEnv)
			if value == "" {
				// The config knows this variable is needed; the operator has
				// not supplied it. Saying which local variable to set is the
				// difference between a plan and a shrug.
				out.Add(plan.Action{
					Op: plan.OpManual, Resource: "render-env", Target: want.Key,
					Detail: fmt.Sprintf("set $%s locally, then re-run", want.ValueEnv),
				})
				continue
			}
		}

		switch {
		case !present:
			out.Add(setAction(api, r.ServiceID, want, value, plan.OpCreate,
				describe(want, "not set on the service")))
		case current != value:
			out.Add(setAction(api, r.ServiceID, want, value, plan.OpUpdate,
				describe(want, "differs from the config")))
		}
	}
	return out, nil
}

// describe says what is wrong without ever saying what the value is.
func describe(want config.EnvVar, what string) string {
	switch {
	case want.ValueFrom != "":
		return fmt.Sprintf("%s (value from %s, not shown)", what, want.ValueFrom)
	case want.Secret():
		return fmt.Sprintf("%s (value from $%s, not shown)", what, want.ValueEnv)
	}
	return fmt.Sprintf("%s → %s", what, want.Value)
}

func setAction(api Setter, serviceID string, want config.EnvVar, value string, op plan.Op, detail string) plan.Action {
	return plan.Action{
		Op: op, Resource: "render-env", Target: want.Key, Detail: detail,
		Do: func(ctx context.Context) error {
			return api.SetEnvVar(ctx, serviceID, want.Key, value)
		},
	}
}
