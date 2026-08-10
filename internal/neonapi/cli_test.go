package neonapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func credentials(expiresAt time.Time) []byte {
	return []byte(fmt.Sprintf(
		`{"access_token":"tok-1","refresh_token":"r","expires_at":%d,"token_type":"Bearer"}`,
		expiresAt.UnixMilli()))
}

func TestANeonctlSessionIsRead(t *testing.T) {
	s, ok := parseCredentials(credentials(time.Now().Add(time.Hour)))
	if !ok || s.Token != "tok-1" {
		t.Fatalf("got %+v ok=%v", s, ok)
	}
	if s.Expired {
		t.Error("an hour of life left is not expired")
	}
}

// neonctl stores epoch MILLISECONDS. Reading them as seconds would date every
// session to 1970 and report every one of them expired.
func TestExpiryIsReadAsMilliseconds(t *testing.T) {
	when := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	s, _ := parseCredentials(credentials(when))
	if !s.Expires.Equal(when) {
		t.Errorf("expiry read as %s, want %s", s.Expires, when)
	}
}

func TestAnExpiredSessionSaysWhatToDo(t *testing.T) {
	s, _ := parseCredentials(credentials(time.Now().Add(-time.Minute)))
	if !s.Expired {
		t.Fatal("a minute past expiry is expired")
	}
	if !strings.Contains(s.ExpiredMessage(), "neonctl auth") {
		t.Errorf("got %q", s.ExpiredMessage())
	}
}

func TestATokenAboutToExpireCountsAsExpired(t *testing.T) {
	s, _ := parseCredentials(credentials(time.Now().Add(20 * time.Second)))
	if !s.Expired {
		t.Error("20 seconds of life left should not be handed to a run")
	}
}

func TestRubbishIsNotASession(t *testing.T) {
	for _, raw := range []string{"", "{}", "not json", `{"access_token":""}`} {
		if _, ok := parseCredentials([]byte(raw)); ok {
			t.Errorf("%q should not read as a session", raw)
		}
	}
}
