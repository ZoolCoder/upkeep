// Package flysecrets plans a Fly.io app's secrets.
//
// It is the second hosting provider, and it exists partly to prove the seam is
// a seam: Render's planner and this one share no code, only an interface.
//
// The important difference is what the provider will tell you. Render returns
// a variable's value, so upkeep can say "this differs from the config". Fly
// returns a name and a digest and never a value, so upkeep can say a secret is
// missing and it can set one — but it cannot say an existing one is wrong. Every
// plan here is worded to claim only what was actually checked.
package flysecrets

import (
	"context"
	"fmt"
	"sort"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/flyapi"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/secret"
)

// API is the half of Fly upkeep uses.
type API interface {
	Secrets(ctx context.Context, app string) (map[string]flyapi.Secret, error)
	SetSecret(ctx context.Context, app, name, value string) error
}

var _ API = (*flyapi.Client)(nil)

// Plan diffs the declared secrets against the names the app has.
func Plan(
	ctx context.Context,
	api API,
	f config.Fly,
	getenv func(string) string,
	secrets *secret.Set,
) (plan.Plan, error) {
	var out plan.Plan

	live, err := api.Secrets(ctx, f.App)
	if err != nil {
		return out, fmt.Errorf("read %s secrets: %w", f.App, err)
	}

	declared := make([]config.EnvVar, len(f.Secrets))
	copy(declared, f.Secrets)
	sort.Slice(declared, func(i, j int) bool { return declared[i].Key < declared[j].Key })

	for _, want := range declared {
		_, present := live[want.Key]

		if want.Manual {
			if present {
				continue
			}
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "fly-secret", Target: want.Key,
				Detail: want.Why,
			})
			continue
		}

		value, problem := resolve(ctx, want, getenv, secrets)
		if problem != "" {
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "fly-secret", Target: want.Key,
				Detail: problem,
			})
			continue
		}

		if present {
			// Deliberately silent. Fly never returns a value, so upkeep has
			// checked that the secret EXISTS and nothing more. Reporting it as
			// matching would claim a comparison that never happened.
			continue
		}
		out.Add(plan.Action{
			Op: plan.OpCreate, Resource: "fly-secret", Target: want.Key,
			Detail: describe(want),
			Do: func(ctx context.Context) error {
				return api.SetSecret(ctx, f.App, want.Key, value)
			},
		})
	}
	return out, nil
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

// describe never prints a value, the same rule every other surface follows.
func describe(want config.EnvVar) string {
	const missing = "not set on the app"
	switch {
	case want.ValueFrom != "":
		return fmt.Sprintf("%s (value from %s, not shown)", missing, want.ValueFrom)
	case want.ValueEnv != "":
		return fmt.Sprintf("%s (value from $%s, not shown)", missing, want.ValueEnv)
	}
	return missing + " → " + want.Value
}
