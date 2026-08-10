package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// A Saved plan is what you reviewed, written down.
//
// `upkeep apply` on its own re-reads live state, so what runs is not
// necessarily what you read a minute ago — someone else's deploy, a dashboard
// edit, or a provider's own housekeeping can move the world in between. Saving
// the plan and applying THAT closes the gap, the way `terraform plan -out` does.
//
// It records intent rather than closures: an Action carries a func, which
// cannot be serialised, and a file full of instructions to run blind would be a
// worse thing to hand a machine than the config was. On apply, upkeep re-plans
// from the same config and refuses unless the new plan is identical to the
// saved one. Same guarantee, and it fails loudly when the world moved instead
// of quietly doing something you never saw.
type Saved struct {
	Version int           `json:"version"`
	Actions []SavedAction `json:"actions"`
}

// SavedAction is one line of a reviewed plan. It holds no values, for the same
// reason nothing else here does.
type SavedAction struct {
	Op       string `json:"op"`
	Resource string `json:"resource"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
}

const savedVersion = 1

// Save writes the plan for a later apply.
func (p Plan) Save(w io.Writer) error {
	out := Saved{Version: savedVersion, Actions: p.savedActions()}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func (p Plan) savedActions() []SavedAction {
	out := make([]SavedAction, 0, len(p.Actions))
	for _, a := range p.sorted() {
		out = append(out, SavedAction{
			Op: string(a.Op), Resource: a.Resource, Target: a.Target, Detail: a.Detail,
		})
	}
	return out
}

// LoadSaved reads a plan written by Save.
func LoadSaved(r io.Reader) (Saved, error) {
	var saved Saved
	if err := json.NewDecoder(r).Decode(&saved); err != nil {
		return Saved{}, fmt.Errorf("read saved plan: %w", err)
	}
	if saved.Version != savedVersion {
		return Saved{}, fmt.Errorf(
			"saved plan is version %d, this upkeep writes version %d", saved.Version, savedVersion)
	}
	return saved, nil
}

// Matches reports whether a freshly read plan is the one that was reviewed.
//
// The comparison is every field including the detail, because the detail is
// where the reason lives: "not set on the service" and "differs from the
// config" are different facts about the same key, and a reviewer who approved
// one did not approve the other.
func (s Saved) Matches(fresh Plan) error {
	now := fresh.savedActions()
	if len(now) != len(s.Actions) {
		return fmt.Errorf(
			"the live state changed since this plan was saved: it had %d action(s), there are now %d — re-run plan",
			len(s.Actions), len(now))
	}
	for i := range now {
		if now[i] != s.Actions[i] {
			return fmt.Errorf(
				"the live state changed since this plan was saved.\n  reviewed: %s\n  now:      %s\nre-run plan",
				describe(s.Actions[i]), describe(now[i]))
		}
	}
	return nil
}

func describe(a SavedAction) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s %s — %s", a.Op, a.Resource, a.Target, a.Detail))
}

// Write renders a saved plan the same way a live one is rendered, so reviewing
// a file and reviewing a run look identical.
func (s Saved) Write(w io.Writer) error {
	p := Plan{}
	for _, a := range s.Actions {
		p.Actions = append(p.Actions, Action{
			Op: Op(a.Op), Resource: a.Resource, Target: a.Target, Detail: a.Detail,
		})
	}
	sort.SliceStable(p.Actions, func(i, j int) bool {
		if p.Actions[i].Resource != p.Actions[j].Resource {
			return p.Actions[i].Resource < p.Actions[j].Resource
		}
		return p.Actions[i].Target < p.Actions[j].Target
	})
	return p.Write(w)
}
