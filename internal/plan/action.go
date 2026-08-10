// Package plan holds the ordered list of changes upkeep intends to make.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Op string

const (
	OpCreate Op = "CREATE"
	OpUpdate Op = "UPDATE"
	OpDelete Op = "DELETE"
	// OpManual is a change a human must complete outside upkeep, because a
	// credential can only be minted by hand or a value is nobody's to invent.
	// It renders in the plan but is never executed, so a converged plan that
	// still lists a manual action is not a failure to converge.
	//
	// This is the whole reason the tool is worth having. An R2 API token cannot
	// be created from any CLI — the permission does not exist on an OAuth
	// session — so a tool that could only automate would report success on a
	// deployment whose photo upload was dead. Naming the gap IS the feature.
	OpManual Op = "MANUAL"
)

// Action is one intended change. Do performs it and must be idempotent: a rerun
// after a partial apply has to be safe. Do is nil exactly when Op is OpManual.
type Action struct {
	Op       Op
	Resource string // render-env, render-deploy, r2-bucket, r2-cors, r2-public, pages, neon
	Target   string // which service, bucket, project — the thing being changed
	Detail   string // human-readable, and NEVER a secret value
	Do       func(context.Context) error
}

type Plan struct {
	Actions []Action
}

func (p *Plan) Add(actions ...Action) {
	p.Actions = append(p.Actions, actions...)
}

func (p *Plan) Extend(other Plan) {
	p.Actions = append(p.Actions, other.Actions...)
}

func (p Plan) Empty() bool { return len(p.Actions) == 0 }

// sorted is a stable order, so two runs of an unchanged config produce
// identical output and a diff between them means something.
func (p Plan) sorted() []Action {
	out := make([]Action, len(p.Actions))
	copy(out, p.Actions)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// Manual returns the actions no tool can perform. A caller that reports success
// should still surface these, or it is reporting a half-built deployment as a
// finished one.
func (p Plan) Manual() []Action {
	var out []Action
	for _, a := range p.Actions {
		if a.Op == OpManual {
			out = append(out, a)
		}
	}
	return out
}

// Executable is everything with a Do — what apply will actually run.
func (p Plan) Executable() []Action {
	var out []Action
	for _, a := range p.Actions {
		if a.Op != OpManual {
			out = append(out, a)
		}
	}
	return out
}

// Destructive reports whether the plan deletes anything, so a caller can insist
// on confirmation before running it.
func (p Plan) Destructive() bool {
	for _, a := range p.Actions {
		if a.Op == OpDelete {
			return true
		}
	}
	return false
}

// Write renders the plan as aligned columns, grouped by resource, in a stable
// order so two runs of an unchanged config produce identical output.
func (p Plan) Write(w io.Writer) error {
	if p.Empty() {
		_, err := fmt.Fprintln(w, "no changes: everything matches the config")
		return err
	}

	sorted := p.sorted()
	opWidth, resWidth, targetWidth := 0, 0, 0
	for _, a := range sorted {
		opWidth = max(opWidth, len(a.Op))
		resWidth = max(resWidth, len(a.Resource))
		targetWidth = max(targetWidth, len(a.Target))
	}

	var b strings.Builder
	for _, a := range sorted {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n",
			opWidth, string(a.Op), resWidth, a.Resource, targetWidth, a.Target, a.Detail)
	}
	if manual := p.Manual(); len(manual) > 0 {
		fmt.Fprintf(&b, "\n%d of these cannot be automated and are yours to do.\n", len(manual))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON renders the plan for something other than a person: a CI step
// deciding whether to fail, a dashboard, a diff between two runs.
//
// Detail is included and values are not, which is the same rule the text
// rendering follows — a machine-readable plan that leaked what the text one
// hides would be a way around the guarantee rather than a second view of it.
func (p Plan) WriteJSON(w io.Writer) error {
	type action struct {
		Op       string `json:"op"`
		Resource string `json:"resource"`
		Target   string `json:"target"`
		Detail   string `json:"detail"`
		Manual   bool   `json:"manual"`
	}
	out := struct {
		Changes int      `json:"changes"`
		Manual  int      `json:"manual"`
		Actions []action `json:"actions"`
	}{Changes: len(p.Executable()), Manual: len(p.Manual()), Actions: []action{}}

	for _, a := range p.sorted() {
		out.Actions = append(out.Actions, action{
			Op: string(a.Op), Resource: a.Resource, Target: a.Target,
			Detail: a.Detail, Manual: a.Op == OpManual,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
