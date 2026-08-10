package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func noop(context.Context) error { return nil }

// A machine-readable plan that leaked what the text one hides would be a way
// around the guarantee rather than a second view of it.
func TestJsonAndTextAgreeOnWhatTheySay(t *testing.T) {
	p := Plan{Actions: []Action{{
		Op: OpUpdate, Resource: "render-env", Target: "R2_SECRET_ACCESS_KEY",
		Detail: "differs from the config (value from $R2_SECRET, not shown)",
	}}}

	var text, encoded strings.Builder
	if err := p.Write(&text); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteJSON(&encoded); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), encoded.String()} {
		if strings.Contains(out, "hunter2") {
			t.Error("a value reached the output")
		}
		if !strings.Contains(out, "$R2_SECRET") {
			t.Errorf("both views name the source, got %q", out)
		}
	}
}

func TestJsonCountsChangesAndManualSeparately(t *testing.T) {
	p := Plan{Actions: []Action{
		{Op: OpCreate, Resource: "render-env", Target: "A", Do: noop},
		{Op: OpUpdate, Resource: "render-env", Target: "B", Do: noop},
		{Op: OpManual, Resource: "render-env", Target: "C", Detail: "yours"},
	}}

	var out strings.Builder
	if err := p.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Changes int `json:"changes"`
		Manual  int `json:"manual"`
		Actions []struct {
			Target string `json:"target"`
			Manual bool   `json:"manual"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Changes != 2 || got.Manual != 1 {
		t.Errorf("changes=%d manual=%d", got.Changes, got.Manual)
	}
	if len(got.Actions) != 3 {
		t.Fatalf("expected every action listed, got %d", len(got.Actions))
	}
	for _, a := range got.Actions {
		if (a.Target == "C") != a.Manual {
			t.Errorf("%s manual=%v", a.Target, a.Manual)
		}
	}
}

// An empty plan must be valid JSON with an empty list, not null: a CI step
// doing `.actions | length` should not have to special-case success.
func TestAnEmptyPlanIsStillAnObject(t *testing.T) {
	var out strings.Builder
	if err := (Plan{}).WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"actions": []`) {
		t.Errorf("expected an empty list, got %s", out.String())
	}
}

// Two runs of an unchanged config must produce identical output, or a diff
// between them means nothing.
func TestOutputOrderIsStable(t *testing.T) {
	p := Plan{Actions: []Action{
		{Op: OpUpdate, Resource: "r2-cors", Target: "z"},
		{Op: OpCreate, Resource: "render-env", Target: "b"},
		{Op: OpCreate, Resource: "render-env", Target: "a"},
	}}
	var first, second strings.Builder
	if err := p.Write(&first); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(&second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Error("two renderings of one plan differ")
	}
	lines := strings.Split(strings.TrimSpace(first.String()), "\n")
	// Sorted by resource, then target — and "r2-cors" precedes "render-env"
	// because "r2" precedes "re".
	want := []string{"r2-cors     z", "render-env  a", "render-env  b"}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Errorf("line %d is %q, expected %q", i, lines[i], w)
		}
	}
}

func TestDestructiveIsOnlyTrueForDeletes(t *testing.T) {
	if (Plan{Actions: []Action{{Op: OpUpdate}, {Op: OpManual}}}).Destructive() {
		t.Error("no delete, not destructive")
	}
	if !(Plan{Actions: []Action{{Op: OpDelete}}}).Destructive() {
		t.Error("a delete is destructive")
	}
}
