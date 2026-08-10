// Package secret resolves a value from somewhere other than the environment.
//
// `valueEnv` covers one person: the value is already exported in their shell.
// It does not cover a team, where the value lives in a password manager and
// nobody should be exporting it by hand in the first place.
//
// Nothing in this package writes to stdout, and no resolved value is ever
// returned to anything that renders.
package secret

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Resolver fetches one value by reference.
type Resolver interface {
	// Scheme is the prefix that selects this resolver, without "://".
	Scheme() string
	// Resolve returns the value for a reference, or an error explaining what a
	// human should do about it.
	Resolve(ctx context.Context, ref string) (string, error)
}

// Set is the resolvers available to a run.
type Set struct {
	byScheme map[string]Resolver
}

func NewSet(resolvers ...Resolver) *Set {
	s := &Set{byScheme: map[string]Resolver{}}
	for _, r := range resolvers {
		s.byScheme[r.Scheme()] = r
	}
	return s
}

// Default is what the CLI uses.
func Default() *Set { return NewSet(OnePassword{}) }

// Schemes lists what this build can resolve, for an error that helps.
func (s *Set) Schemes() []string {
	out := make([]string, 0, len(s.byScheme))
	for scheme := range s.byScheme {
		out = append(out, scheme+"://")
	}
	return out
}

// Resolve dispatches a reference to its resolver.
func (s *Set) Resolve(ctx context.Context, ref string) (string, error) {
	scheme, _, ok := strings.Cut(ref, "://")
	if !ok {
		return "", fmt.Errorf(
			"%q is not a reference; it should look like op://vault/item/field", ref)
	}
	resolver, known := s.byScheme[scheme]
	if !known {
		return "", fmt.Errorf(
			"no resolver for %s:// — this build has %s",
			scheme, strings.Join(s.Schemes(), ", "))
	}
	value, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s resolved to an empty value", ref)
	}
	return value, nil
}

// opRef is deliberately strict. The reference comes from a config file, which
// may have come from a repository, so it is untrusted input to a subprocess.
// Commands are run without a shell — there is no interpolation to inject into —
// and this narrows it further to the shape 1Password documents.
var opRef = regexp.MustCompile(`^op://[A-Za-z0-9 ._-]+/[A-Za-z0-9 ._-]+/[A-Za-z0-9 ._/-]+$`)

// OnePassword reads through the `op` CLI, so upkeep never handles a vault
// credential itself: `op` is already signed in, or it is not and says so.
type OnePassword struct {
	// run is injected for tests. Nil means exec.
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func (OnePassword) Scheme() string { return "op" }

func (o OnePassword) Resolve(ctx context.Context, ref string) (string, error) {
	if !opRef.MatchString(ref) {
		return "", fmt.Errorf(
			"%q is not a 1Password reference; it should look like op://vault/item/field", ref)
	}
	run := o.run
	if run == nil {
		run = execCommand
	}
	out, err := run(ctx, "op", "read", "--no-newline", ref)
	if err != nil {
		return "", fmt.Errorf(
			"reading %s from 1Password failed (%w) — is `op` installed and signed in?", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// execCommand runs without a shell, so a reference cannot become an argument
// list or a redirection.
func execCommand(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(args[0]); err != nil {
		return nil, fmt.Errorf("%s is not installed", args[0])
	}
	return exec.CommandContext(ctx, args[0], args[1:]...).Output()
}
