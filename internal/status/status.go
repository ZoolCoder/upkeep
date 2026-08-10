// Package status answers "what is live right now" without diffing anything.
//
// A plan answers a different question — how does the world differ from this
// file — and answering the first with the second means reading a list of
// changes and inferring the state behind it. This reads the state and says it.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/config"
)

// Render is the read half of the service provider.
type Render interface {
	EnvVars(ctx context.Context, serviceID string) (map[string]string, error)
}

// CF is the Cloudflare transport.
type CF interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

// App is what one app looks like right now. Every field is a fact read from a
// provider, or the reason it could not be.
type App struct {
	Name     string    `json:"name"`
	Surfaces []Surface `json:"surfaces"`
}

// Surface is one place an app lives.
type Surface struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// State is a short human phrase: "12 variables", "public, 1 CORS rule".
	State string `json:"state"`
	// Problem is set when the surface could not be read, and is never empty
	// just because something is absent — absent is a state, not a failure.
	Problem string `json:"problem,omitempty"`
}

// Read gathers the state of every app given.
func Read(ctx context.Context, apps []config.App, render Render, cf CF) []App {
	out := make([]App, 0, len(apps))
	for _, app := range apps {
		out = append(out, App{Name: app.Name, Surfaces: surfaces(ctx, app, render, cf)})
	}
	return out
}

func surfaces(ctx context.Context, app config.App, render Render, cf CF) []Surface {
	var out []Surface

	if r := app.Render; r != nil {
		s := Surface{Kind: "render", Target: r.ServiceID}
		switch {
		case render == nil:
			s.Problem = "no credential"
		default:
			env, err := render.EnvVars(ctx, r.ServiceID)
			if err != nil {
				s.Problem = err.Error()
			} else {
				s.State = fmt.Sprintf("%d variable(s)", len(env))
				if missing := missingKeys(r.Env, env); len(missing) > 0 {
					s.State += fmt.Sprintf(", %d not set: %s",
						len(missing), strings.Join(missing, " "))
				}
			}
		}
		out = append(out, s)
	}

	if b := app.R2; b != nil {
		s := Surface{Kind: "r2", Target: b.Bucket}
		switch {
		case cf == nil:
			s.Problem = "no credential"
		default:
			s.State = bucketState(ctx, cf, *b)
		}
		out = append(out, s)
	}

	if p := app.Pages; p != nil {
		s := Surface{Kind: "pages", Target: p.Project}
		switch {
		case cf == nil:
			s.Problem = "no credential"
		default:
			s.State = pagesState(ctx, cf, *p)
		}
		out = append(out, s)
	}

	if n := app.Neon; n != nil {
		out = append(out, Surface{
			Kind: "neon", Target: n.ProjectID,
			State: "branch " + orDash(n.Branch),
		})
	}
	return out
}

// missingKeys names the declared variables the service does not have. This is
// the one question status answers that a dashboard does not: which of the
// things this app needs are absent.
func missingKeys(declared []config.EnvVar, live map[string]string) []string {
	var out []string
	for _, e := range declared {
		if live[e.Key] == "" {
			out = append(out, e.Key)
		}
	}
	sort.Strings(out)
	return out
}

func bucketState(ctx context.Context, cf CF, b config.R2) string {
	base := fmt.Sprintf("/accounts/%s/r2/buckets/%s", b.AccountID, b.Bucket)

	var managed struct {
		Enabled bool `json:"enabled"`
	}
	access := "private"
	if err := cf.Do(ctx, "GET", base+"/domains/managed", nil, &managed); err == nil && managed.Enabled {
		access = "public"
	}

	var cors struct {
		Rules []json.RawMessage `json:"rules"`
	}
	rules := 0
	if err := cf.Do(ctx, "GET", base+"/cors", nil, &cors); err == nil {
		rules = len(cors.Rules)
	}
	return fmt.Sprintf("%s, %d CORS rule(s)", access, rules)
}

func pagesState(ctx context.Context, cf CF, p config.Pages) string {
	var live struct {
		ProductionBranch string   `json:"production_branch"`
		Domains          []string `json:"domains"`
		Latest           struct {
			ShortID string `json:"short_id"`
			Stage   struct {
				Status string `json:"status"`
			} `json:"latest_stage"`
		} `json:"latest_deployment"`
	}
	if err := cf.Do(ctx, "GET",
		fmt.Sprintf("/accounts/%s/pages/projects/%s", p.AccountID, p.Project), nil, &live); err != nil {
		return "not found"
	}
	state := fmt.Sprintf("branch %s, %d domain(s)", orDash(live.ProductionBranch), len(live.Domains))
	if live.Latest.ShortID != "" {
		state += fmt.Sprintf(", last deploy %s %s", live.Latest.ShortID, live.Latest.Stage.Status)
	}
	return state
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Write renders the state as aligned columns.
func Write(w io.Writer, apps []App) error {
	var b strings.Builder
	for _, app := range apps {
		fmt.Fprintf(&b, "%s\n", app.Name)
		kindWidth, targetWidth := 0, 0
		for _, s := range app.Surfaces {
			kindWidth = max(kindWidth, len(s.Kind))
			targetWidth = max(targetWidth, len(s.Target))
		}
		for _, s := range app.Surfaces {
			state := s.State
			if s.Problem != "" {
				state = "unreadable: " + s.Problem
			}
			fmt.Fprintf(&b, "  %-*s  %-*s  %s\n", kindWidth, s.Kind, targetWidth, s.Target, state)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON renders the same for something other than a person.
func WriteJSON(w io.Writer, apps []App) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Apps []App `json:"apps"`
	}{Apps: apps})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
