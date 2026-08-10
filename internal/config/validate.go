package config

import (
	"fmt"
	"strings"
)

// Validate rejects a config that would produce a confusing failure later.
//
// Everything here is a mistake that is cheap to make and expensive to diagnose
// from the other end: a variable with no source reaches Render as an empty
// string, a CORS rule with no origin blocks every browser, an app with no
// surfaces produces an empty plan that looks like success.
func Validate(cfg Config) error {
	if cfg.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", cfg.Version)
	}
	if len(cfg.Apps) == 0 {
		return fmt.Errorf("config declares no apps")
	}

	seen := map[string]bool{}
	for i, app := range cfg.Apps {
		where := fmt.Sprintf("apps[%d]", i)
		if app.Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		where = fmt.Sprintf("app %q", app.Name)
		if seen[app.Name] {
			return fmt.Errorf("%s: declared twice", where)
		}
		seen[app.Name] = true

		if app.Render == nil && app.R2 == nil && app.Pages == nil &&
			app.Neon == nil && app.DNS == nil && app.Auth == nil &&
			app.Fly == nil && app.Workers == nil {
			return fmt.Errorf("%s: declares no surfaces, so there is nothing to reconcile", where)
		}
		if err := validateRender(where, app.Render); err != nil {
			return err
		}
		if err := validateR2(where, app.R2); err != nil {
			return err
		}
		if err := validatePages(where, app.Pages); err != nil {
			return err
		}
		if err := validateNeon(where, app.Neon); err != nil {
			return err
		}
		if err := validateDNS(where, app.DNS); err != nil {
			return err
		}
		if err := validateAuth(where, app.Auth); err != nil {
			return err
		}
		if err := validateFly(where, app.Fly); err != nil {
			return err
		}
		if err := validateWorkers(where, app.Workers); err != nil {
			return err
		}
	}
	return nil
}

func validateRender(where string, r *Render) error {
	if r == nil {
		return nil
	}
	if r.ServiceID == "" {
		return fmt.Errorf("%s: render.serviceId is required", where)
	}
	if !strings.HasPrefix(r.ServiceID, "srv-") {
		return fmt.Errorf("%s: render.serviceId %q does not look like a service id (srv-…)", where, r.ServiceID)
	}
	return validateEnvVars(where, r.Env)
}

func validateR2(where string, r *R2) error {
	if r == nil {
		return nil
	}
	if r.AccountID == "" {
		return fmt.Errorf("%s: r2.accountId is required", where)
	}
	if r.Bucket == "" {
		return fmt.Errorf("%s: r2.bucket is required", where)
	}
	for i, rule := range r.CORS {
		at := fmt.Sprintf("%s: r2.cors[%d]", where, i)
		if len(rule.Origins) == 0 {
			return fmt.Errorf("%s: no origins, which allows nothing at all", at)
		}
		if len(rule.Methods) == 0 {
			return fmt.Errorf("%s: no methods", at)
		}
		for _, o := range rule.Origins {
			if o != "*" && !strings.Contains(o, "://") {
				return fmt.Errorf("%s: origin %q has no scheme; CORS matches the whole origin", at, o)
			}
			if strings.HasSuffix(o, "/") {
				return fmt.Errorf("%s: origin %q has a trailing slash, which never matches", at, o)
			}
		}
	}
	return nil
}

func validatePages(where string, p *Pages) error {
	if p == nil {
		return nil
	}
	if p.AccountID == "" {
		return fmt.Errorf("%s: pages.accountId is required", where)
	}
	if p.Project == "" {
		return fmt.Errorf("%s: pages.project is required", where)
	}
	return nil
}

func validateNeon(where string, n *Neon) error {
	if n == nil {
		return nil
	}
	if n.ProjectID == "" {
		return fmt.Errorf("%s: neon.projectId is required", where)
	}
	return nil
}

// authProviders are the names a frontend build can carry. A typo here would
// silently disable the one check that catches a frontend and backend built for
// different providers.
var authProviders = []string{"local", "neon", "oidc", "supabase", "fake"}

func validateAuth(where string, a *Auth) error {
	if a == nil {
		return nil
	}
	if a.FrontendProvider != "" {
		known := false
		for _, p := range authProviders {
			if strings.EqualFold(a.FrontendProvider, p) {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("%s: auth.frontendProvider %q is not one of %s",
				where, a.FrontendProvider, strings.Join(authProviders, ", "))
		}
	}
	if a.FrontendAuthURL != "" && !strings.Contains(a.FrontendAuthURL, "://") {
		return fmt.Errorf("%s: auth.frontendAuthUrl %q needs a scheme", where, a.FrontendAuthURL)
	}
	for _, o := range a.SiteOrigins {
		if !strings.Contains(o, "://") {
			return fmt.Errorf("%s: auth.siteOrigins %q has no scheme; the match is on a whole origin", where, o)
		}
	}
	return nil
}

// validateEnvVars is the one rule for a variable that carries a value, used by
// every provider that has them. Two copies would drift, and the copy that
// drifted would be the one that let a credential into a committed file.
func validateEnvVars(where string, vars []EnvVar) error {
	keys := map[string]bool{}
	for _, e := range vars {
		if e.Key == "" {
			return fmt.Errorf("%s: an env entry has no key", where)
		}
		if keys[e.Key] {
			return fmt.Errorf("%s: env %s is declared twice", where, e.Key)
		}
		keys[e.Key] = true

		// The likeliest way to leak a secret with this tool is to paste one
		// into a value: and commit the file. The config cannot know what a
		// value IS, but it knows what the key is called, and a key called
		// SECRET or TOKEN or PASSWORD holding a literal is almost always that
		// mistake. Refuse it at parse time, before it is ever written to a
		// repository, and say which field to use instead.
		if e.Value != "" && LooksSecret(e.Key) {
			return fmt.Errorf(
				"%s: env %s carries a literal value, and its name says it is a credential — "+
					"use `valueEnv: %s` so the value stays out of this file, or `manual: true` "+
					"if a human sets it",
				where, e.Key, e.Key)
		}

		sources := 0
		if e.Value != "" {
			sources++
		}
		if e.ValueEnv != "" {
			sources++
		}
		if e.ValueFrom != "" {
			sources++
		}
		if e.Manual {
			sources++
		}
		switch sources {
		case 1:
		case 0:
			return fmt.Errorf(
				"%s: env %s has no value, valueEnv, valueFrom or manual — an unsourced variable reaches the service as an empty string",
				where, e.Key)
		default:
			return fmt.Errorf("%s: env %s names more than one source", where, e.Key)
		}
		if e.ValueFrom != "" && !strings.Contains(e.ValueFrom, "://") {
			return fmt.Errorf(
				"%s: env %s valueFrom %q is not a reference; it should look like op://vault/item/field",
				where, e.Key, e.ValueFrom)
		}
		if e.Manual && e.Why == "" {
			return fmt.Errorf(
				"%s: env %s is manual with no why — a plan that says something is missing without saying what to do is not worth printing",
				where, e.Key)
		}
	}
	return nil
}

func validateWorkers(where string, w *Workers) error {
	if w == nil {
		return nil
	}
	if w.AccountID == "" {
		return fmt.Errorf("%s: workers.accountId is required", where)
	}
	if w.Script == "" {
		return fmt.Errorf("%s: workers.script is required", where)
	}
	if len(w.Routes) > 0 && w.ZoneID == "" {
		return fmt.Errorf(
			"%s: workers.routes needs workers.zoneId — routes live on a zone, not on the script", where)
	}
	for i, r := range w.Routes {
		if r == "" {
			return fmt.Errorf("%s: workers.routes[%d] is empty", where, i)
		}
		// A route with no host matches nothing and is silent about it.
		if !strings.Contains(r, "/") && !strings.Contains(r, "*") {
			return fmt.Errorf(
				"%s: workers.routes[%d] %q is not a route pattern; it should look like example.com/api/*",
				where, i, r)
		}
	}
	return validateEnvVars(where+" workers.secrets", w.Secrets)
}

func validateFly(where string, f *Fly) error {
	if f == nil {
		return nil
	}
	if f.App == "" {
		return fmt.Errorf("%s: fly.app is required", where)
	}
	// Same rules as a Render variable: the file is meant to be committed.
	return validateEnvVars(where+" fly.secrets", f.Secrets)
}

func validateDNS(where string, d *DNS) error {
	if d == nil {
		return nil
	}
	if d.ZoneID == "" {
		return fmt.Errorf("%s: dns.zoneId is required", where)
	}
	seen := map[string]bool{}
	for i, r := range d.Records {
		at := fmt.Sprintf("%s: dns.records[%d]", where, i)
		if r.Type == "" || r.Name == "" || r.Content == "" {
			return fmt.Errorf("%s: type, name and content are all required", at)
		}
		// A record is identified by type and name, so two of the same pair are
		// two intentions for one record and upkeep cannot know which wins.
		k := strings.ToUpper(r.Type) + " " + strings.ToLower(r.Name)
		if seen[k] {
			return fmt.Errorf("%s: %s is declared twice", at, k)
		}
		seen[k] = true
		if r.TTL != 0 && r.TTL < 60 {
			return fmt.Errorf("%s: ttl %d is below Cloudflare's minimum of 60 (0 means automatic)", at, r.TTL)
		}
	}
	return nil
}

// secretNames are the substrings that make a variable's name a claim about its
// contents. Deliberately eager: a public value wrongly refused costs one line
// of config, a secret wrongly committed costs a rotation.
//
// A value that genuinely is public and unluckily named — PUBLIC_KEY, say — can
// still be set through valueEnv, so the escape hatch is never "commit it".
var secretNames = []string{
	"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
	"PRIVATE_KEY", "SIGNING_KEY", "ACCESS_KEY", "API_KEY",
	"DATABASE_URL", "DSN", "CONNECTION_STRING",
}

// LooksSecret is exported because the importer must agree with this exactly:
// if it emitted a literal for a key validation refuses, it would write a file
// the tool cannot read back.
func LooksSecret(key string) bool {
	upper := strings.ToUpper(key)
	for _, name := range secretNames {
		if strings.Contains(upper, name) {
			return true
		}
	}
	return false
}
