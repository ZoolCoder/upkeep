// Package report turns a plan into something worth waking up to.
//
// `plan` answers "what differs" for one operator at a keyboard. A scheduled run
// answers a different question for someone who was not watching: across every
// app, what changed, what can still be fixed automatically, and what has been
// waiting on a human — and for how long.
//
// The distinction that makes it readable is the one the plan already draws.
// Actionable items are a to-do list. Manual items are a standing debt, and a
// report that mixes them is a log people skim rather than a page they read.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/plan"
)

// Report is one run, across every app.
type Report struct {
	// Actionable is what upkeep could close on the next apply.
	Actionable []Item `json:"actionable"`
	// Outstanding is what no tool can do. It is expected to persist, which is
	// why it is counted separately rather than treated as a failure.
	Outstanding []Item `json:"outstanding"`
}

type Item struct {
	Op       string `json:"op"`
	Resource string `json:"resource"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
}

// From splits a plan into the two halves that read differently.
func From(p plan.Plan) Report {
	r := Report{Actionable: []Item{}, Outstanding: []Item{}}
	for _, a := range p.Actions {
		item := Item{Op: string(a.Op), Resource: a.Resource, Target: a.Target, Detail: a.Detail}
		if a.Op == plan.OpManual {
			r.Outstanding = append(r.Outstanding, item)
			continue
		}
		r.Actionable = append(r.Actionable, item)
	}
	sortItems(r.Actionable)
	sortItems(r.Outstanding)
	return r
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Resource != items[j].Resource {
			return items[i].Resource < items[j].Resource
		}
		return items[i].Target < items[j].Target
	})
}

// Quiet reports whether there is nothing to say. A scheduled run that produces
// no output when everything is fine is one people leave switched on.
func (r Report) Quiet() bool {
	return len(r.Actionable) == 0 && len(r.Outstanding) == 0
}

// Headline is the one line that has to survive being read on a phone.
func (r Report) Headline() string {
	switch {
	case r.Quiet():
		return "everything matches the config"
	case len(r.Actionable) == 0:
		return fmt.Sprintf("%d item(s) waiting on a human", len(r.Outstanding))
	case len(r.Outstanding) == 0:
		return fmt.Sprintf("%d change(s) upkeep can make", len(r.Actionable))
	}
	return fmt.Sprintf("%d change(s) upkeep can make, %d waiting on a human",
		len(r.Actionable), len(r.Outstanding))
}

// Write renders the report for a person.
func (r Report) Write(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintln(&b, r.Headline())

	if len(r.Actionable) > 0 {
		fmt.Fprintf(&b, "\nupkeep apply would close these:\n")
		writeItems(&b, r.Actionable)
	}
	if len(r.Outstanding) > 0 {
		// Second, deliberately. These are the ones that will still be here next
		// week, and putting them first trains people to scroll past the part
		// that changed.
		fmt.Fprintf(&b, "\nNo tool can do these:\n")
		writeItems(&b, r.Outstanding)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeItems(b *strings.Builder, items []Item) {
	resWidth, targetWidth := 0, 0
	for _, i := range items {
		resWidth = max(resWidth, len(i.Resource))
		targetWidth = max(targetWidth, len(i.Target))
	}
	for _, i := range items {
		fmt.Fprintf(b, "  %-*s  %-*s  %s\n", resWidth, i.Resource, targetWidth, i.Target, i.Detail)
	}
}

// WriteJSON renders it for something else — a dashboard, a diff between two
// runs, a bot that only speaks when the actionable count is above zero.
func (r Report) WriteJSON(w io.Writer) error {
	out := struct {
		Headline    string `json:"headline"`
		Actionable  int    `json:"actionableCount"`
		Outstanding int    `json:"outstandingCount"`
		Report
	}{r.Headline(), len(r.Actionable), len(r.Outstanding), r}

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
