package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Diff describes a change to something with structure, in terms of what moved
// rather than what the result should be.
//
// "differs from the config: https://a https://b allow PUT with content-type"
// restates the destination and leaves the reader to spot the difference
// themselves — against a rule with four origins and six headers, nobody does.
// "+cache-control" is the whole change.
type Diff struct {
	Added   []string
	Removed []string
	Changed []Change
}

// Change is one field whose value moved. Neither side is ever a secret: this is
// for lists of origins, methods and header names.
type Change struct {
	Field string
	From  string
	To    string
}

// SetDiff compares two unordered lists of names.
func SetDiff(field string, from, to []string) Diff {
	have := index(from)
	want := index(to)

	var d Diff
	for _, name := range sorted(to) {
		if !have[name] {
			d.Added = append(d.Added, field+" "+name)
		}
	}
	for _, name := range sorted(from) {
		if !want[name] {
			d.Removed = append(d.Removed, field+" "+name)
		}
	}
	return d
}

// Merge folds another diff into this one.
func (d *Diff) Merge(other Diff) {
	d.Added = append(d.Added, other.Added...)
	d.Removed = append(d.Removed, other.Removed...)
	d.Changed = append(d.Changed, other.Changed...)
}

// Set records a scalar that moved.
func (d *Diff) Set(field, from, to string) {
	if from != to {
		d.Changed = append(d.Changed, Change{Field: field, From: from, To: to})
	}
}

func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// String renders the difference for a plan line: additions, removals, then
// anything that moved.
func (d Diff) String() string {
	parts := make([]string, 0, len(d.Added)+len(d.Removed)+len(d.Changed))
	for _, a := range d.Added {
		parts = append(parts, "+"+a)
	}
	for _, r := range d.Removed {
		parts = append(parts, "-"+r)
	}
	for _, c := range d.Changed {
		parts = append(parts, fmt.Sprintf("%s %s→%s", c.Field, c.From, c.To))
	}
	return strings.Join(parts, ", ")
}

func index(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[strings.ToLower(n)] = true
	}
	return out
}

func sorted(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.ToLower(n))
	}
	sort.Strings(out)
	return out
}
