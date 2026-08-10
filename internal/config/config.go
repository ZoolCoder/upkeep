// Package config is the declared shape of an app's cloud footprint.
//
// One file describes every surface an app occupies — the Render service and its
// environment, the R2 bucket that holds its uploads, the Pages project that
// serves its frontend, the Neon database behind it — and upkeep's job is to
// make the cloud match it, or say plainly which part it cannot.
package config

// Config is one file, describing one or more apps.
type Config struct {
	Version int   `yaml:"version"`
	Apps    []App `yaml:"apps"`
}

// App is everything one deployed product occupies.
type App struct {
	Name   string  `yaml:"name"`
	Render *Render `yaml:"render"`
	R2     *R2     `yaml:"r2"`
	Pages  *Pages  `yaml:"pages"`
	Neon   *Neon   `yaml:"neon"`
	DNS    *DNS    `yaml:"dns"`
	Auth   *Auth   `yaml:"auth"`
}

// Auth is the one surface that checks variables against each other rather than
// against a provider. Authentication fails when two values disagree, not when
// one is missing, and nothing else notices.
//
// upkeep never changes any of it: a wrong automatic fix here locks out
// everybody, including whoever ran the tool.
type Auth struct {
	// FrontendProvider is what the frontend was BUILT with — it is baked into
	// the bundle and cannot be read back from anywhere, so declaring it is the
	// only way to check the two halves agree. One of local, neon, oidc,
	// supabase, fake.
	FrontendProvider string `yaml:"frontendProvider"`
	// FrontendAuthURL is where the browser sends its session calls. Probed,
	// because the usual failure is a URL that lost its path and now 404s —
	// which a client reads as "no session".
	FrontendAuthURL string `yaml:"frontendAuthUrl"`
	// SiteOrigins must each appear in the service's CORS allowlist. The match
	// is exact, so a hostname that is nearly right is entirely wrong.
	SiteOrigins []string `yaml:"siteOrigins"`
}

// DNS is the handful of records a website needs in order to exist. Not a zone
// manager: records upkeep does not declare are reported and never removed,
// because a DNS record is the one thing here that can take a site off the
// internet by being deleted.
type DNS struct {
	ZoneID  string      `yaml:"zoneId"`
	Records []DNSRecord `yaml:"records"`
}

type DNSRecord struct {
	Type    string `yaml:"type"`
	Name    string `yaml:"name"`
	Content string `yaml:"content"`
	// TTL in seconds. Zero means Cloudflare's automatic.
	TTL int `yaml:"ttl"`
	// Proxied routes through Cloudflare. Unset leaves the zone's own choice
	// alone rather than asserting one.
	Proxied *bool `yaml:"proxied"`
}

// Render is the backend service: which one, and what its environment must be.
type Render struct {
	ServiceID string `yaml:"serviceId"`
	// Env is the service's environment. Absent keys are left alone unless the
	// run is pruning — a service carries variables the platform put there.
	Env []EnvVar `yaml:"env"`
	// Image, when set, is what a deploy ships. Empty means upkeep never
	// triggers one and only reconciles the environment.
	Image string `yaml:"image"`
}

// EnvVar is one variable. Exactly one of Value or ValueEnv is set.
//
// The split is the point. A literal is checked into the repo because it is not
// a secret — an endpoint, a bucket name, a public hostname. A secret names the
// local environment variable it comes from, so the value passes through memory
// and never appears in the config, the plan, or a terminal.
type EnvVar struct {
	Key string `yaml:"key"`
	// Value is a literal, for things that are not secret.
	Value string `yaml:"value"`
	// ValueEnv names a local environment variable holding the value.
	ValueEnv string `yaml:"valueEnv"`
	// ValueFrom names a reference in a secret manager, e.g.
	// op://vault/item/field. For a team, where the value should not be
	// exported into anyone's shell in the first place.
	ValueFrom string `yaml:"valueFrom"`
	// Manual marks a value nobody can generate: real bank details, a
	// credential only a dashboard can mint. upkeep reports it missing and
	// never invents one.
	Manual bool `yaml:"manual"`
	// Why explains a manual variable to whoever reads the plan. It is the
	// difference between "something is missing" and "here is what to do".
	Why string `yaml:"why"`
}

// Secret reports whether this variable's value must never be printed.
func (e EnvVar) Secret() bool { return e.ValueEnv != "" || e.ValueFrom != "" || e.Manual }

// R2 is a bucket and the two settings that decide whether a browser can use it.
type R2 struct {
	AccountID string `yaml:"accountId"`
	Bucket    string `yaml:"bucket"`
	// Public serves objects on the bucket's r2.dev hostname.
	Public bool `yaml:"public"`
	// CORS is what a browser is allowed to do directly against the bucket.
	// Empty means upkeep leaves whatever is there alone.
	CORS []CORSRule `yaml:"cors"`
}

// CORSRule mirrors R2's own shape rather than S3's, because that is what the
// API accepts and a translation layer is one more thing to get wrong.
type CORSRule struct {
	Origins []string `yaml:"origins"`
	Methods []string `yaml:"methods"`
	// Headers must list every header the client sends. For a presigned upload
	// that means every header in the signature — a signed header the preflight
	// refuses never leaves the page.
	Headers       []string `yaml:"headers"`
	ExposeHeaders []string `yaml:"exposeHeaders"`
	MaxAgeSeconds int      `yaml:"maxAgeSeconds"`
}

// Pages is the frontend project.
type Pages struct {
	AccountID string `yaml:"accountId"`
	Project   string `yaml:"project"`
	// ProductionBranch decides which deployment is the real one. It matters
	// more than it looks: a preview URL is a different origin, and an exact
	// CORS or allowlist match refuses it.
	ProductionBranch string `yaml:"productionBranch"`
	// Domains is every hostname that should serve this project. upkeep
	// reports ones it does not know about rather than deleting them.
	Domains []string `yaml:"domains"`
}

// Neon is the database. upkeep reads it and reports drift; it does not
// create or drop projects, because a database is not a thing to converge
// silently.
type Neon struct {
	ProjectID string `yaml:"projectId"`
	Branch    string `yaml:"branch"`
	// DatabaseURLEnv names the local variable holding the connection string,
	// when the Render service's DATABASE_URL should match it.
	DatabaseURLEnv string `yaml:"databaseUrlEnv"`
}
