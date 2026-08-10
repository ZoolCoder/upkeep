package neonapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CLISession is a credential upkeep borrowed from neonctl rather than
// asking the operator for a second one.
type CLISession struct {
	Token   string
	Expires time.Time
	// Expired is checked rather than assumed: an expired token fails as a 401,
	// which reads like a permission problem and sends people looking at their
	// Neon account instead of at `neonctl auth`.
	Expired bool
}

// neonctl writes an OAuth response verbatim, so expires_at is epoch
// milliseconds rather than the RFC 3339 string wrangler stores.
type storedCredentials struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

func credentialPaths() []string {
	var out []string
	if dir := os.Getenv("NEON_CONFIG_DIR"); dir != "" {
		out = append(out, filepath.Join(dir, "credentials.json"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	out = append(out,
		filepath.Join(home, ".config", "neonctl", "credentials.json"),
		filepath.Join(home, ".neon", "credentials.json"),
	)
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "neonctl", "credentials.json"))
	}
	return out
}

// TokenFromCLI reads the session `neonctl auth` already stored.
//
// It returns nothing rather than an error when neonctl has never been used:
// that is not a problem to report, it is simply one fewer place to look.
func TokenFromCLI() (CLISession, bool) {
	for _, path := range credentialPaths() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if session, ok := parseCredentials(raw); ok {
			return session, true
		}
	}
	return CLISession{}, false
}

func parseCredentials(raw []byte) (CLISession, bool) {
	var stored storedCredentials
	if err := json.Unmarshal(raw, &stored); err != nil || stored.AccessToken == "" {
		return CLISession{}, false
	}
	session := CLISession{Token: stored.AccessToken}
	if stored.ExpiresAt > 0 {
		session.Expires = time.UnixMilli(stored.ExpiresAt)
		// A minute of slack: a token that expires mid-run is worse than one
		// reported expired a moment early.
		session.Expired = time.Now().Add(time.Minute).After(session.Expires)
	}
	return session, true
}

// ExpiredMessage says what to do, rather than leaving a 401 to be diagnosed.
func (s CLISession) ExpiredMessage() string {
	return fmt.Sprintf(
		"neonctl's session expired at %s — run `neonctl auth`, or set NEON_API_KEY",
		s.Expires.Local().Format(time.RFC3339))
}
