// Package dns plans Cloudflare DNS records.
//
// The narrowest useful scope on purpose: the handful of records a website needs
// to exist — an apex, a www, a verification TXT. Not a zone manager. Records
// upkeep does not know about are left alone and reported, because a DNS record
// is the one thing here that can take a site off the internet by being removed.
package dns

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

type API interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

var _ API = (*cfapi.Client)(nil)

type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied *bool  `json:"proxied,omitempty"`
}

// Plan diffs the declared records against the zone.
func Plan(ctx context.Context, api API, d config.DNS) (plan.Plan, error) {
	var out plan.Plan
	base := "/zones/" + d.ZoneID + "/dns_records"

	var live []record
	if err := api.Do(ctx, "GET", base+"?per_page=100", nil, &live); err != nil {
		return out, fmt.Errorf("read zone %s: %w", d.ZoneID, err)
	}

	byKey := map[string]record{}
	for _, r := range live {
		byKey[key(r.Type, r.Name)] = r
	}

	declared := make([]config.DNSRecord, len(d.Records))
	copy(declared, d.Records)
	sort.Slice(declared, func(i, j int) bool {
		return key(declared[i].Type, declared[i].Name) < key(declared[j].Type, declared[j].Name)
	})

	wanted := map[string]bool{}
	for _, want := range declared {
		k := key(want.Type, want.Name)
		wanted[k] = true
		body := record{
			Type: strings.ToUpper(want.Type), Name: want.Name,
			Content: want.Content, TTL: ttlOr(want.TTL),
		}
		if want.Proxied != nil {
			body.Proxied = want.Proxied
		}

		current, exists := byKey[k]
		switch {
		case !exists:
			out.Add(plan.Action{
				Op: plan.OpCreate, Resource: "dns", Target: label(want),
				Detail: "does not exist → " + want.Content,
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "POST", base, body, nil)
				},
			})
		case differs(current, body):
			id := current.ID
			out.Add(plan.Action{
				Op: plan.OpUpdate, Resource: "dns", Target: label(want),
				Detail: describe(current, body).String(),
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "PUT", base+"/"+id, body, nil)
				},
			})
		}
	}

	// Anything else in the zone is reported and never touched. A record with
	// traffic behind it is not a typo to act on, and upkeep has no way to know
	// what a record it did not declare is for.
	var extra []string
	for _, r := range live {
		if !wanted[key(r.Type, r.Name)] {
			extra = append(extra, r.Type+" "+r.Name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		out.Add(plan.Action{
			Op: plan.OpManual, Resource: "dns", Target: d.ZoneID,
			Detail: fmt.Sprintf("%d record(s) upkeep does not manage, left alone: %s",
				len(extra), strings.Join(extra, ", ")),
		})
	}
	return out, nil
}

// differs compares only what upkeep sets. Cloudflare returns fields it computes
// itself, and treating those as drift would offer the same change forever.
func differs(live, want record) bool {
	if !strings.EqualFold(live.Content, want.Content) {
		return true
	}
	if live.TTL != want.TTL {
		return true
	}
	if want.Proxied != nil && (live.Proxied == nil || *live.Proxied != *want.Proxied) {
		return true
	}
	return false
}

func describe(live, want record) plan.Diff {
	var d plan.Diff
	d.Set("content", live.Content, want.Content)
	d.Set("ttl", fmt.Sprint(live.TTL), fmt.Sprint(want.TTL))
	if want.Proxied != nil {
		d.Set("proxied", fmt.Sprint(live.Proxied != nil && *live.Proxied), fmt.Sprint(*want.Proxied))
	}
	return d
}

// ttlOr defaults to 1, which is Cloudflare's "automatic". Zero would be
// rejected, and picking a number here would silently override the zone.
func ttlOr(ttl int) int {
	if ttl == 0 {
		return 1
	}
	return ttl
}

func key(recordType, name string) string {
	return strings.ToUpper(recordType) + " " + strings.ToLower(name)
}

func label(r config.DNSRecord) string {
	return strings.ToUpper(r.Type) + " " + r.Name
}
