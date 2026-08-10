package cfapi

import (
	"strings"
	"testing"
	"time"
)

const wranglerToml = `oauth_token = "abc.def-ghi"
expiration_time = "%s"
refresh_token = "rrr"
scopes = [ "account:read", "user:read" ]
`

func config(expires time.Time) string {
	return strings.Replace(wranglerToml, "%s", expires.Format(time.RFC3339), 1)
}

func TestAWranglerSessionIsRead(t *testing.T) {
	s, ok := parseWranglerConfig(config(time.Now().Add(time.Hour)))
	if !ok {
		t.Fatal("expected a session")
	}
	if s.Token != "abc.def-ghi" {
		t.Errorf("token is %q", s.Token)
	}
	if s.Expired {
		t.Error("a session valid for an hour is not expired")
	}
}

// An expired OAuth token fails as a 401, which reads like a permission problem
// and sends people looking at their Cloudflare account instead of at
// `wrangler login`.
func TestAnExpiredSessionSaysSo(t *testing.T) {
	s, ok := parseWranglerConfig(config(time.Now().Add(-time.Hour)))
	if !ok {
		t.Fatal("expected a session")
	}
	if !s.Expired {
		t.Fatal("an hour past expiry is expired")
	}
	if !strings.Contains(s.ExpiredMessage(), "wrangler login") {
		t.Errorf("the message should say what to do, got %q", s.ExpiredMessage())
	}
}

// A token expiring mid-run is worse than one reported expired a moment early.
func TestATokenAboutToExpireCountsAsExpired(t *testing.T) {
	s, _ := parseWranglerConfig(config(time.Now().Add(20 * time.Second)))
	if !s.Expired {
		t.Error("20 seconds of life left should not be handed to a run")
	}
}

func TestAConfigWithoutATokenIsNotASession(t *testing.T) {
	for _, text := range []string{
		"",
		"refresh_token = \"rrr\"\n",
		"oauth_token = \"\"\n",
	} {
		if _, ok := parseWranglerConfig(text); ok {
			t.Errorf("%q should not read as a session", text)
		}
	}
}

// wrangler has written a config with no expiry in the past; a session that
// cannot be dated is used rather than refused, and fails honestly if stale.
func TestAnUndatedSessionIsStillUsable(t *testing.T) {
	s, ok := parseWranglerConfig(`oauth_token = "abc"` + "\n")
	if !ok || s.Token != "abc" {
		t.Fatal("expected the token")
	}
	if s.Expired {
		t.Error("no expiry is not an expired expiry")
	}
}
