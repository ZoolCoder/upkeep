package secret

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func opWith(out string, err error) OnePassword {
	return OnePassword{run: func(context.Context, ...string) ([]byte, error) {
		return []byte(out), err
	}}
}

func TestAReferenceIsRead(t *testing.T) {
	set := NewSet(opWith("hunter2\n", nil))
	got, err := set.Resolve(context.Background(), "op://Private/render/api-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q", got)
	}
}

// The reference comes from a config file, which may have come from a
// repository. Commands run without a shell, so there is nothing to interpolate
// into — and the shape is narrowed further to what 1Password documents, so a
// reference carrying shell metacharacters never reaches a subprocess at all.
func TestAMalformedReferenceIsRefusedBeforeAnythingRuns(t *testing.T) {
	ran := false
	op := OnePassword{run: func(context.Context, ...string) ([]byte, error) {
		ran = true
		return nil, nil
	}}
	set := NewSet(op)

	for _, ref := range []string{
		"op://vault/item; id",
		"op://vault/item/field$(whoami)",
		"op://vault/item/field`id`",
		"op://vault/item/field|tee /etc/passwd",
		"op://vault/item/field&&curl evil.example",
		"op://vault",
		"op://",
		"op://vault/item/field\nop://other",
	} {
		if _, err := set.Resolve(context.Background(), ref); err == nil {
			t.Errorf("%q should be refused", ref)
		}
	}
	if ran {
		t.Error("a malformed reference reached the subprocess")
	}
}

func TestAnUnknownSchemeSaysWhatIsAvailable(t *testing.T) {
	set := NewSet(OnePassword{})
	_, err := set.Resolve(context.Background(), "vault://secret/thing")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "op://") {
		t.Errorf("it should list what this build has, got %v", err)
	}
}

func TestSomethingThatIsNotAReferenceAtAll(t *testing.T) {
	_, err := Default().Resolve(context.Background(), "just-a-value")
	if err == nil || !strings.Contains(err.Error(), "op://vault/item/field") {
		t.Fatalf("got %v", err)
	}
}

// A vault that answers with nothing is not a value; setting an empty string on
// a service is how a feature goes quietly dead.
func TestAnEmptyResultIsAnError(t *testing.T) {
	set := NewSet(opWith("", nil))
	if _, err := set.Resolve(context.Background(), "op://Private/render/api-key"); err == nil {
		t.Fatal("an empty value should be refused")
	}
}

// The error has to tell a human what to do, not merely that something failed.
func TestAFailureSaysWhatToCheck(t *testing.T) {
	set := NewSet(opWith("", errors.New("exit status 1")))
	_, err := set.Resolve(context.Background(), "op://Private/render/api-key")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "signed in") {
		t.Errorf("got %v", err)
	}
}

func TestAValidReferenceWithSpacesIsAccepted(t *testing.T) {
	// 1Password vault and item names routinely contain spaces.
	set := NewSet(opWith("v", nil))
	if _, err := set.Resolve(context.Background(), "op://My Vault/Render API/credential"); err != nil {
		t.Fatal(err)
	}
}
