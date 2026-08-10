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

func awsWith(out string, err error) AWSSecretsManager {
	return AWSSecretsManager{run: func(context.Context, ...string) ([]byte, error) {
		return []byte(out), err
	}}
}

func gcpWith(out string, err error) GoogleSecretManager {
	return GoogleSecretManager{run: func(context.Context, ...string) ([]byte, error) {
		return []byte(out), err
	}}
}

func TestTheDefaultSetCarriesEveryScheme(t *testing.T) {
	schemes := strings.Join(Default().Schemes(), " ")
	for _, want := range []string{"op://", "aws://", "gcp://"} {
		if !strings.Contains(schemes, want) {
			t.Errorf("%s missing from %q", want, schemes)
		}
	}
}

func TestAnAwsSecretIsRead(t *testing.T) {
	got, err := NewSet(awsWith("hunter2\n", nil)).
		Resolve(context.Background(), "aws://prod/render-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q", got)
	}
}

// A secret holding a JSON document is the common shape; naming a field beats
// making every caller store one value per secret.
func TestAFieldIsPulledOutOfAJsonSecret(t *testing.T) {
	set := NewSet(awsWith(`{"username":"u","password":"p2"}`, nil))
	got, err := set.Resolve(context.Background(), "aws://prod/db#password")
	if err != nil {
		t.Fatal(err)
	}
	if got != "p2" {
		t.Errorf("got %q", got)
	}
}

func TestANamedFieldThatIsNotThere(t *testing.T) {
	set := NewSet(awsWith(`{"username":"u"}`, nil))
	_, err := set.Resolve(context.Background(), "aws://prod/db#password")
	if err == nil || !strings.Contains(err.Error(), "no field") {
		t.Fatalf("got %v", err)
	}
}

func TestANamedFieldOnASecretThatIsNotJson(t *testing.T) {
	set := NewSet(awsWith("just-a-string", nil))
	_, err := set.Resolve(context.Background(), "aws://prod/db#password")
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("got %v", err)
	}
}

func TestAGoogleSecretIsRead(t *testing.T) {
	got, err := NewSet(gcpWith("hunter2\n", nil)).
		Resolve(context.Background(), "gcp://my-project/render-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q", got)
	}
}

// A secret may legitimately end in whitespace. Trimming it silently produces a
// value that works nowhere and looks right everywhere.
func TestAGoogleSecretKeepsInteriorWhitespace(t *testing.T) {
	got, err := NewSet(gcpWith("-----BEGIN KEY-----\nabc\n-----END KEY-----\n", nil)).
		Resolve(context.Background(), "gcp://my-project/signing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\nabc\n") {
		t.Errorf("the newlines inside were lost: %q", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("only the trailing newline gcloud adds should go")
	}
}

// Every resolver narrows its reference before anything runs, because a config
// may have come from a repository.
func TestNoResolverLetsAMetacharacterReachASubprocess(t *testing.T) {
	ran := false
	spy := func(context.Context, ...string) ([]byte, error) {
		ran = true
		return nil, nil
	}
	set := NewSet(
		OnePassword{run: spy},
		AWSSecretsManager{run: spy},
		GoogleSecretManager{run: spy},
	)

	for _, ref := range []string{
		"aws://prod/db; id", "aws://prod/db$(whoami)", "aws://prod/db`id`",
		"aws://", "aws://prod/db#field with spaces",
		"gcp://my project/secret", "gcp://proj/secret; id", "gcp://proj",
		"gcp://PROJ/secret", "op://vault/item/field|tee /tmp/x",
	} {
		if _, err := set.Resolve(context.Background(), ref); err == nil {
			t.Errorf("%q should be refused", ref)
		}
	}
	if ran {
		t.Error("a malformed reference reached a subprocess")
	}
}

func TestEachFailureSaysWhichToolToCheck(t *testing.T) {
	for _, c := range []struct{ ref, want string }{
		{"aws://prod/db", "aws CLI"},
		{"gcp://proj/secret", "gcloud"},
		{"op://vault/item/field", "op"},
	} {
		set := NewSet(
			awsWith("", errors.New("exit 1")),
			gcpWith("", errors.New("exit 1")),
			OnePassword{run: func(context.Context, ...string) ([]byte, error) {
				return nil, errors.New("exit 1")
			}},
		)
		_, err := set.Resolve(context.Background(), c.ref)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, wanted a mention of %s", c.ref, err, c.want)
		}
	}
}
