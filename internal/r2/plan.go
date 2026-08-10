// Package r2 plans a bucket and the two settings that decide whether a browser
// can actually use it.
//
// Both are invisible from the application's side. A bucket with public access
// off still accepts uploads and then serves nothing; a bucket with the wrong
// CORS rule fails the preflight, so the request never leaves the page and the
// error the app sees is indistinguishable from a bad credential.
package r2

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/plan"
)

// API is the transport, narrowed so tests can supply their own.
type API interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

var _ API = (*cfapi.Client)(nil)

// The R2 API's own CORS shape. Not S3's — wrangler rejects the S3 form with a
// link to the docs, and a translation layer here would be one more thing to get
// subtly wrong.
type corsRule struct {
	Allowed struct {
		Origins []string `json:"origins"`
		Methods []string `json:"methods"`
		Headers []string `json:"headers,omitempty"`
	} `json:"allowed"`
	ExposeHeaders []string `json:"exposeHeaders,omitempty"`
	MaxAgeSeconds int      `json:"maxAgeSeconds,omitempty"`
}

type corsDoc struct {
	Rules []corsRule `json:"rules"`
}

type managedDomain struct {
	Enabled bool   `json:"enabled"`
	Domain  string `json:"domain"`
}

// Plan diffs the declared bucket against the live one.
func Plan(ctx context.Context, api API, r config.R2) (plan.Plan, error) {
	var out plan.Plan
	base := fmt.Sprintf("/accounts/%s/r2/buckets/%s", r.AccountID, r.Bucket)

	exists, err := bucketExists(ctx, api, r)
	if err != nil {
		return out, err
	}
	if !exists {
		out.Add(plan.Action{
			Op: plan.OpCreate, Resource: "r2-bucket", Target: r.Bucket,
			Detail: "does not exist",
			Do: func(ctx context.Context) error {
				return api.Do(ctx, "POST",
					fmt.Sprintf("/accounts/%s/r2/buckets", r.AccountID),
					map[string]string{"name": r.Bucket}, nil)
			},
		})
		// Everything below reads settings of a bucket that is not there yet.
		// Plan them anyway, in order, so one apply converges.
	}

	if r.Public {
		var live managedDomain
		err := api.Do(ctx, "GET", base+"/domains/managed", nil, &live)
		switch {
		case err != nil && !cfapi.NotConfigured(err):
			return out, fmt.Errorf("read %s public access: %w", r.Bucket, err)
		case err != nil || !live.Enabled:
			out.Add(plan.Action{
				Op: plan.OpUpdate, Resource: "r2-public", Target: r.Bucket,
				Detail: "public r2.dev access is off, so uploaded objects serve nothing",
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "PUT", base+"/domains/managed",
						map[string]bool{"enabled": true}, nil)
				},
			})
		}
	}

	if len(r.CORS) > 0 {
		want := corsDocFor(r.CORS)
		var live corsDoc
		err := api.Do(ctx, "GET", base+"/cors", nil, &live)
		if err != nil && !cfapi.NotConfigured(err) {
			return out, fmt.Errorf("read %s CORS: %w", r.Bucket, err)
		}
		missing := err != nil
		if missing || !reflect.DeepEqual(normalise(live), normalise(want)) {
			detail := "differs from the config: " + corsDiff(live, want).String()
			if missing {
				detail = "no CORS rule at all, so a browser cannot upload: " + summarise(want)
			}
			out.Add(plan.Action{
				Op: plan.OpUpdate, Resource: "r2-cors", Target: r.Bucket, Detail: detail,
				Do: func(ctx context.Context) error {
					return api.Do(ctx, "PUT", base+"/cors", want, nil)
				},
			})
		}
	}
	return out, nil
}

func bucketExists(ctx context.Context, api API, r config.R2) (bool, error) {
	err := api.Do(ctx, "GET",
		fmt.Sprintf("/accounts/%s/r2/buckets/%s", r.AccountID, r.Bucket), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case cfapi.NotConfigured(err):
		return false, nil
	default:
		return false, fmt.Errorf("read bucket %s: %w", r.Bucket, err)
	}
}

func corsDocFor(rules []config.CORSRule) corsDoc {
	var doc corsDoc
	for _, r := range rules {
		var out corsRule
		out.Allowed.Origins = r.Origins
		out.Allowed.Methods = r.Methods
		out.Allowed.Headers = r.Headers
		out.ExposeHeaders = r.ExposeHeaders
		out.MaxAgeSeconds = r.MaxAgeSeconds
		doc.Rules = append(doc.Rules, out)
	}
	return doc
}

// normalise sorts every list so a reordered rule is not a change. R2 returns
// them in its own order, and re-writing an identical policy on every run would
// make the plan lie about what it is doing.
func normalise(doc corsDoc) corsDoc {
	out := corsDoc{Rules: make([]corsRule, len(doc.Rules))}
	copy(out.Rules, doc.Rules)
	for i := range out.Rules {
		out.Rules[i].Allowed.Origins = sorted(out.Rules[i].Allowed.Origins)
		out.Rules[i].Allowed.Methods = sorted(lowered(out.Rules[i].Allowed.Methods))
		out.Rules[i].Allowed.Headers = sorted(lowered(out.Rules[i].Allowed.Headers))
		out.Rules[i].ExposeHeaders = sorted(lowered(out.Rules[i].ExposeHeaders))
	}
	sort.Slice(out.Rules, func(i, j int) bool {
		return strings.Join(out.Rules[i].Allowed.Origins, ",") <
			strings.Join(out.Rules[j].Allowed.Origins, ",")
	})
	return out
}

// Header names are case-insensitive in CORS; R2 lowercases what it stores, so
// a config written as Content-Type must not read as a difference forever.
func lowered(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func sorted(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// corsDiff says what moved rather than restating the whole desired rule. A
// missing allowed header is the difference between an upload working and
// failing, and it is one word in a line the reader would otherwise have to
// compare by eye.
func corsDiff(live, want corsDoc) plan.Diff {
	var d plan.Diff
	l, w := normalise(live), normalise(want)

	// Rules are compared pairwise in sorted order. Beyond a first rule this is
	// approximate, and says so by falling back to a count.
	for i := range w.Rules {
		if i >= len(l.Rules) {
			d.Added = append(d.Added, fmt.Sprintf("rule for %s",
				strings.Join(w.Rules[i].Allowed.Origins, " ")))
			continue
		}
		d.Merge(plan.SetDiff("origin", l.Rules[i].Allowed.Origins, w.Rules[i].Allowed.Origins))
		d.Merge(plan.SetDiff("method", l.Rules[i].Allowed.Methods, w.Rules[i].Allowed.Methods))
		d.Merge(plan.SetDiff("header", l.Rules[i].Allowed.Headers, w.Rules[i].Allowed.Headers))
		d.Merge(plan.SetDiff("expose", l.Rules[i].ExposeHeaders, w.Rules[i].ExposeHeaders))
		if l.Rules[i].MaxAgeSeconds != w.Rules[i].MaxAgeSeconds {
			d.Set("maxAge",
				fmt.Sprint(l.Rules[i].MaxAgeSeconds), fmt.Sprint(w.Rules[i].MaxAgeSeconds))
		}
	}
	for i := len(w.Rules); i < len(l.Rules); i++ {
		d.Removed = append(d.Removed, fmt.Sprintf("rule for %s",
			strings.Join(l.Rules[i].Allowed.Origins, " ")))
	}
	if d.Empty() {
		// Equal by every field upkeep compares, but DeepEqual disagreed —
		// report honestly rather than printing nothing.
		return plan.Diff{Changed: []plan.Change{{Field: "rules", From: "live", To: "config"}}}
	}
	return d
}

func summarise(doc corsDoc) string {
	var parts []string
	for _, r := range doc.Rules {
		parts = append(parts, fmt.Sprintf("%s allow %s with %s",
			strings.Join(r.Allowed.Origins, " "),
			strings.Join(r.Allowed.Methods, ","),
			strings.Join(r.Allowed.Headers, ",")))
	}
	return strings.Join(parts, "; ")
}
