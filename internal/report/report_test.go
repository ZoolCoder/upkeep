package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/plan"
)

func sample() plan.Plan {
	return plan.Plan{Actions: []plan.Action{
		{Op: plan.OpCreate, Resource: "render-env", Target: "R2_BUCKET", Detail: "not set"},
		{Op: plan.OpManual, Resource: "render-env", Target: "FEE_ACCOUNT_INFO", Detail: "real bank details"},
		{Op: plan.OpUpdate, Resource: "r2-cors", Target: "b", Detail: "+header cache-control"},
		{Op: plan.OpManual, Resource: "auth", Target: "AUTH_JWKS_URI", Detail: "served no keys"},
	}}
}

// The split is the whole point: a to-do list and a standing debt read
// differently, and mixing them makes a page people skim.
func TestTheTwoHalvesAreSeparated(t *testing.T) {
	r := From(sample())
	if len(r.Actionable) != 2 || len(r.Outstanding) != 2 {
		t.Fatalf("actionable=%d outstanding=%d", len(r.Actionable), len(r.Outstanding))
	}
	for _, i := range r.Outstanding {
		if i.Op != string(plan.OpManual) {
			t.Errorf("outstanding must be manual, got %+v", i)
		}
	}
	for _, i := range r.Actionable {
		if i.Op == string(plan.OpManual) {
			t.Errorf("actionable must not be manual, got %+v", i)
		}
	}
}

// What upkeep can still fix comes first. The manual items will be there next
// week too, and leading with them trains people to scroll past what changed.
func TestActionableIsPrintedFirst(t *testing.T) {
	var out strings.Builder
	if err := From(sample()).Write(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	canFix, cannot := strings.Index(text, "would close these"), strings.Index(text, "No tool can do")
	if canFix < 0 || cannot < 0 {
		t.Fatalf("both sections should appear:\n%s", text)
	}
	if canFix > cannot {
		t.Errorf("what can be fixed should come first:\n%s", text)
	}
}

// A scheduled run that says nothing when everything is fine is one people leave
// switched on.
func TestAQuietRunSaysSo(t *testing.T) {
	r := From(plan.Plan{})
	if !r.Quiet() {
		t.Fatal("an empty plan is quiet")
	}
	if r.Headline() != "everything matches the config" {
		t.Errorf("got %q", r.Headline())
	}
}

// The headline has to survive being read on a phone.
func TestTheHeadlineCountsBothKinds(t *testing.T) {
	for _, c := range []struct {
		actionable, manual int
		want               string
	}{
		{2, 2, "2 change(s) upkeep can make, 2 waiting on a human"},
		{0, 3, "3 item(s) waiting on a human"},
		{4, 0, "4 change(s) upkeep can make"},
	} {
		var p plan.Plan
		for i := 0; i < c.actionable; i++ {
			p.Actions = append(p.Actions, plan.Action{Op: plan.OpCreate, Resource: "r", Target: "t"})
		}
		for i := 0; i < c.manual; i++ {
			p.Actions = append(p.Actions, plan.Action{Op: plan.OpManual, Resource: "r", Target: "t"})
		}
		if got := From(p).Headline(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// A report is written to a file, a chat, a dashboard. The redaction rule holds
// wherever a plan's text goes.
func TestAReportCarriesNoValues(t *testing.T) {
	p := plan.Plan{Actions: []plan.Action{{
		Op: plan.OpUpdate, Resource: "render-env", Target: "R2_SECRET_ACCESS_KEY",
		Detail: "differs from the config (value from $R2_SECRET, not shown)",
	}}}
	r := From(p)

	var text, encoded strings.Builder
	if err := r.Write(&text); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteJSON(&encoded); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), encoded.String()} {
		if strings.Contains(out, "hunter2") {
			t.Error("a value reached the output")
		}
		if !strings.Contains(out, "$R2_SECRET") {
			t.Errorf("it should name the source, got %q", out)
		}
	}
}

func TestJsonCarriesCountsAndAHeadline(t *testing.T) {
	var out strings.Builder
	if err := From(sample()).WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Headline    string `json:"headline"`
		Actionable  int    `json:"actionableCount"`
		Outstanding int    `json:"outstandingCount"`
		Items       []struct {
			Target string `json:"target"`
		} `json:"actionable"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if got.Actionable != 2 || got.Outstanding != 2 {
		t.Errorf("got %+v", got)
	}
	if !strings.Contains(got.Headline, "waiting on a human") {
		t.Errorf("headline %q", got.Headline)
	}
}

// An empty report must be valid JSON with empty lists, not null, so a bot doing
// `.actionable | length` needs no special case for the good day.
func TestAnEmptyReportIsStillLists(t *testing.T) {
	var out strings.Builder
	if err := From(plan.Plan{}).WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"actionable": []`) ||
		!strings.Contains(out.String(), `"outstanding": []`) {
		t.Errorf("got %s", out.String())
	}
}

// Two runs of an unchanged world must produce identical output, or a diff
// between them means nothing.
func TestOutputIsStable(t *testing.T) {
	var first, second strings.Builder
	if err := From(sample()).Write(&first); err != nil {
		t.Fatal(err)
	}
	if err := From(sample()).Write(&second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Error("two renderings differ")
	}
}
