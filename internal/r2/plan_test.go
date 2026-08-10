package r2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

// fakeCF answers from a canned map of "METHOD path" → JSON, and records writes.
type fakeCF struct {
	get    map[string]string
	absent map[string]bool // paths that answer "not configured"
	wrote  map[string]any
}

func newCF() *fakeCF {
	return &fakeCF{get: map[string]string{}, absent: map[string]bool{}, wrote: map[string]any{}}
}

func (f *fakeCF) Do(_ context.Context, method, path string, body, result any) error {
	if method != "GET" {
		f.wrote[method+" "+path] = body
		return nil
	}
	if f.absent[path] {
		return &cfapi.Error{Status: 404, Code: 10059, Message: "does not exist", Endpoint: path}
	}
	raw, ok := f.get[path]
	if !ok {
		return nil // exists, nothing to decode
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal([]byte(raw), result)
}

const acct = "acct1"

func base(bucket string) string {
	return fmt.Sprintf("/accounts/%s/r2/buckets/%s", acct, bucket)
}

func planFor(t *testing.T, api API, r config.R2) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func has(p plan.Plan, resource string) (plan.Action, bool) {
	for _, a := range p.Actions {
		if a.Resource == resource {
			return a, true
		}
	}
	return plan.Action{}, false
}

var uploadCORS = []config.CORSRule{{
	Origins:       []string{"https://zoolaqar.pages.dev", "http://localhost:5199"},
	Methods:       []string{"PUT"},
	Headers:       []string{"content-type", "cache-control"},
	MaxAgeSeconds: 3600,
}}

// A bucket with no CORS rule accepts nothing from a browser. The request never
// leaves the page, so the app sees a failure indistinguishable from a bad key.
func TestNoCorsRuleIsPlanned(t *testing.T) {
	api := newCF()
	api.absent[base("zoolaqar")+"/cors"] = true
	api.get[base("zoolaqar")+"/domains/managed"] = `{"enabled":true,"domain":"pub-x.r2.dev"}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", Public: true, CORS: uploadCORS})

	a, ok := has(p, "r2-cors")
	if !ok {
		t.Fatalf("expected a CORS action, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "browser cannot upload") {
		t.Errorf("the detail should say what breaks, got %q", a.Detail)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, wrote := api.wrote["PUT "+base("zoolaqar")+"/cors"]; !wrote {
		t.Error("apply did not write the policy")
	}
}

// The signature commits the browser to sending cache-control. A rule that omits
// it fails the preflight, so this has to read as a difference.
func TestAMissingAllowedHeaderIsADifference(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["https://zoolaqar.pages.dev","http://localhost:5199"],
		"methods":["PUT"],"headers":["content-type"]},"maxAgeSeconds":3600}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: uploadCORS})

	if _, ok := has(p, "r2-cors"); !ok {
		t.Fatal("a missing allowed header should be planned")
	}
}

// R2 returns what it stores in its own order and case. Re-writing an identical
// policy every run would make the plan lie about what it is doing.
func TestAnEquivalentRuleIsNotAChange(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["http://localhost:5199","https://zoolaqar.pages.dev"],
		"methods":["PUT"],"headers":["cache-control","content-type"]},"maxAgeSeconds":3600}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: uploadCORS})

	if !p.Empty() {
		t.Fatalf("reordered and same should be no change, got %+v", p.Actions)
	}
}

// Config may write Content-Type; R2 stores content-type. Header names are
// case-insensitive, and a permanent phantom difference trains people to ignore
// the plan.
func TestHeaderCaseIsNotADifference(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["https://zoolaqar.pages.dev"],
		"methods":["PUT"],"headers":["content-type"]},"maxAgeSeconds":3600}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: []config.CORSRule{{
		Origins: []string{"https://zoolaqar.pages.dev"},
		Methods: []string{"PUT"}, Headers: []string{"Content-Type"}, MaxAgeSeconds: 3600,
	}}})

	if !p.Empty() {
		t.Fatalf("case alone should not be a change, got %+v", p.Actions)
	}
}

// Public access off means uploads succeed and then serve nothing — a failure
// that looks like an upload bug and is not.
func TestPublicAccessOffIsPlanned(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/domains/managed"] = `{"enabled":false}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", Public: true})

	a, ok := has(p, "r2-public")
	if !ok {
		t.Fatalf("expected a public-access action, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "serve nothing") {
		t.Errorf("the detail should say what breaks, got %q", a.Detail)
	}
}

func TestAnAbsentBucketIsCreatedFirst(t *testing.T) {
	api := newCF()
	api.absent[base("new")] = true
	api.absent[base("new")+"/cors"] = true
	api.absent[base("new")+"/domains/managed"] = true

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "new", Public: true, CORS: uploadCORS})

	a, ok := has(p, "r2-bucket")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("expected the bucket to be created, got %+v", p.Actions)
	}
	// One apply has to converge: the bucket, then its two settings.
	if p.Actions[0].Resource != "r2-bucket" {
		t.Errorf("the bucket must be planned first, got %s", p.Actions[0].Resource)
	}
	if len(p.Actions) != 3 {
		t.Errorf("expected bucket + public + cors, got %d", len(p.Actions))
	}
}

// The reader should not have to compare two lists by eye. A missing allowed
// header is one word, and it is the difference between an upload working and
// failing.
func TestTheDiffNamesWhatMoved(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["https://zoolaqar.pages.dev","http://localhost:5199"],
		"methods":["PUT"],"headers":["content-type"]},"maxAgeSeconds":3600}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: uploadCORS})

	a, ok := has(p, "r2-cors")
	if !ok {
		t.Fatal("expected a CORS action")
	}
	if !strings.Contains(a.Detail, "+header cache-control") {
		t.Errorf("the detail should name the added header, got %q", a.Detail)
	}
	// It must not restate what already matches.
	if strings.Contains(a.Detail, "+header content-type") {
		t.Errorf("an unchanged header is not a change, got %q", a.Detail)
	}
}

func TestTheDiffNamesARemovedOrigin(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["https://zoolaqar.pages.dev","http://localhost:5199","https://old.example"],
		"methods":["PUT"],"headers":["content-type","cache-control"]},"maxAgeSeconds":3600}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: uploadCORS})

	a, _ := has(p, "r2-cors")
	if !strings.Contains(a.Detail, "-origin https://old.example") {
		t.Errorf("the detail should name the removed origin, got %q", a.Detail)
	}
}

func TestTheDiffNamesAChangedMaxAge(t *testing.T) {
	api := newCF()
	api.get[base("zoolaqar")+"/cors"] = `{"rules":[{"allowed":{
		"origins":["https://zoolaqar.pages.dev","http://localhost:5199"],
		"methods":["PUT"],"headers":["content-type","cache-control"]},"maxAgeSeconds":60}]}`

	p := planFor(t, api, config.R2{AccountID: acct, Bucket: "zoolaqar", CORS: uploadCORS})

	a, _ := has(p, "r2-cors")
	if !strings.Contains(a.Detail, "maxAge 60→3600") {
		t.Errorf("got %q", a.Detail)
	}
}
