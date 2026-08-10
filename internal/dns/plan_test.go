package dns

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

type fakeZone struct {
	live  string
	wrote map[string]any
}

func (f *fakeZone) Do(_ context.Context, method, path string, body, result any) error {
	if method != "GET" {
		if f.wrote == nil {
			f.wrote = map[string]any{}
		}
		f.wrote[method+" "+path] = body
		return nil
	}
	return json.Unmarshal([]byte(f.live), result)
}

func planFor(t *testing.T, api API, d config.DNS) plan.Plan {
	t.Helper()
	p, err := Plan(context.Background(), api, d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(p plan.Plan, target string) (plan.Action, bool) {
	for _, a := range p.Actions {
		if a.Target == target {
			return a, true
		}
	}
	return plan.Action{}, false
}

func yes() *bool { b := true; return &b }

const zone = `[
  {"id":"r1","type":"A","name":"example.com","content":"192.0.2.1","ttl":1,"proxied":true},
  {"id":"r2","type":"TXT","name":"_verify.example.com","content":"token-1","ttl":300}
]`

func TestMatchingRecordsPlanNothing(t *testing.T) {
	p := planFor(t, &fakeZone{live: zone}, config.DNS{
		ZoneID: "z1",
		Records: []config.DNSRecord{
			{Type: "A", Name: "example.com", Content: "192.0.2.1", Proxied: yes()},
			{Type: "TXT", Name: "_verify.example.com", Content: "token-1", TTL: 300},
		},
	})
	if !p.Empty() {
		t.Fatalf("expected nothing, got %+v", p.Actions)
	}
}

func TestAMissingRecordIsCreated(t *testing.T) {
	api := &fakeZone{live: zone}
	p := planFor(t, api, config.DNS{
		ZoneID: "z1",
		Records: []config.DNSRecord{
			{Type: "CNAME", Name: "www.example.com", Content: "example.com"},
		},
	})
	a, ok := find(p, "CNAME www.example.com")
	if !ok || a.Op != plan.OpCreate {
		t.Fatalf("expected a CREATE, got %+v", p.Actions)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.wrote["POST /zones/z1/dns_records"] == nil {
		t.Error("apply did not create the record")
	}
}

func TestAChangedRecordSaysWhatMoved(t *testing.T) {
	api := &fakeZone{live: zone}
	p := planFor(t, api, config.DNS{
		ZoneID:  "z1",
		Records: []config.DNSRecord{{Type: "A", Name: "example.com", Content: "192.0.2.9", Proxied: yes()}},
	})
	a, ok := find(p, "A example.com")
	if !ok || a.Op != plan.OpUpdate {
		t.Fatalf("expected an UPDATE, got %+v", p.Actions)
	}
	if !strings.Contains(a.Detail, "192.0.2.1→192.0.2.9") {
		t.Errorf("it should name what moved, got %q", a.Detail)
	}
	if err := a.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The record's own id, not a blind create.
	if api.wrote["PUT /zones/z1/dns_records/r1"] == nil {
		t.Errorf("expected an update to r1, wrote %v", api.wrote)
	}
}

// A DNS record is the one thing here that can take a site off the internet by
// being deleted.
func TestUndeclaredRecordsAreReportedAndNeverRemoved(t *testing.T) {
	p := planFor(t, &fakeZone{live: zone}, config.DNS{
		ZoneID:  "z1",
		Records: []config.DNSRecord{{Type: "A", Name: "example.com", Content: "192.0.2.1", Proxied: yes()}},
	})
	a, ok := find(p, "z1")
	if !ok || a.Op != plan.OpManual {
		t.Fatalf("expected a MANUAL report, got %+v", p.Actions)
	}
	if a.Do != nil {
		t.Error("it must not be executable")
	}
	if !strings.Contains(a.Detail, "TXT _verify.example.com") {
		t.Errorf("it should name the record, got %q", a.Detail)
	}
	for _, action := range p.Actions {
		if action.Op == plan.OpDelete {
			t.Fatal("upkeep must never plan a DNS deletion")
		}
	}
}

// Cloudflare computes fields upkeep does not set. Treating those as drift would
// offer the same change forever.
func TestAnUnsetProxiedLeavesTheZonesChoiceAlone(t *testing.T) {
	p := planFor(t, &fakeZone{live: zone}, config.DNS{
		ZoneID:  "z1",
		Records: []config.DNSRecord{{Type: "A", Name: "example.com", Content: "192.0.2.1"}},
	})
	if _, planned := find(p, "A example.com"); planned {
		t.Errorf("proxied was not declared, so it is not drift: %+v", p.Actions)
	}
}

func TestTypeAndNameMatchCaseInsensitively(t *testing.T) {
	p := planFor(t, &fakeZone{live: zone}, config.DNS{
		ZoneID:  "z1",
		Records: []config.DNSRecord{{Type: "a", Name: "EXAMPLE.com", Content: "192.0.2.1", Proxied: yes()}},
	})
	if _, planned := find(p, "A EXAMPLE.com"); planned {
		t.Errorf("case alone is not a difference: %+v", p.Actions)
	}
}
