package cfapi

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// wrangler stores an OAuth session as TOML. Only two fields matter here.
var (
	oauthToken = regexp.MustCompile(`(?m)^\s*oauth_token\s*=\s*"([^"]+)"`)
	expiresAt  = regexp.MustCompile(`(?m)^\s*expiration_time\s*=\s*"([^"]+)"`)
)

// CLISession is a credential upkeep borrowed from wrangler rather than
// asking the operator for a second one.
type CLISession struct {
	Token   string
	Expires time.Time
	// Expired is checked rather than assumed: an expired OAuth token fails as
	// a 401 on the first call, which reads like a permission problem and sends
	// people looking at their account instead of at `wrangler login`.
	Expired bool
}

// wranglerConfigPaths lists where wrangler keeps its session, most likely
// first. The macOS location is the odd one — it uses Preferences rather than
// the XDG directory every other platform gets.
func wranglerConfigPaths() []string {
	var out []string
	if dir := os.Getenv("WRANGLER_CONFIG_DIR"); dir != "" {
		out = append(out, filepath.Join(dir, "config", "default.toml"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	out = append(out,
		filepath.Join(home, "Library", "Preferences", ".wrangler", "config", "default.toml"),
		filepath.Join(home, ".config", ".wrangler", "config", "default.toml"),
		filepath.Join(home, ".wrangler", "config", "default.toml"),
	)
	// Windows: wrangler follows env-paths, which puts config under APPDATA.
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "xdg.config", ".wrangler", "config", "default.toml"))
	}
	return out
}

// TokenFromWrangler reads the session `wrangler login` already stored, so an
// operator who can run wrangler needs no second long-lived key on disk.
//
// It returns nothing rather than an error when wrangler has never been used:
// that is not a problem to report, it is simply one fewer place to look.
func TokenFromWrangler() (CLISession, bool) {
	for _, path := range wranglerConfigPaths() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		session, ok := parseWranglerConfig(string(raw))
		if ok {
			return session, true
		}
	}
	return CLISession{}, false
}

func parseWranglerConfig(text string) (CLISession, bool) {
	m := oauthToken.FindStringSubmatch(text)
	if len(m) < 2 || strings.TrimSpace(m[1]) == "" {
		return CLISession{}, false
	}
	session := CLISession{Token: m[1]}
	if e := expiresAt.FindStringSubmatch(text); len(e) >= 2 {
		if when, err := time.Parse(time.RFC3339, e[1]); err == nil {
			session.Expires = when
			// A minute of slack: a token that expires mid-run is worse than one
			// reported expired a moment early.
			session.Expired = time.Now().Add(time.Minute).After(when)
		}
	}
	return session, true
}

// ExpiredMessage says what to do, rather than leaving a 401 to be diagnosed.
func (s CLISession) ExpiredMessage() string {
	return fmt.Sprintf(
		"wrangler's session expired at %s — run `wrangler login`, or set CLOUDFLARE_API_TOKEN",
		s.Expires.Local().Format(time.RFC3339))
}
