package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoolcoder/upkeep/internal/testfake"
)

// run drives the command exactly as a shell would, against a fake cloud.
//
// Everything below tests the wiring — flags, config loading, provider
// selection, exit paths — which is the part no unit test reaches and the part
// that breaks when two correct pieces are joined wrongly.
func run(t *testing.T, cloud *testfake.Cloud, stdin string, args ...string) (string, string, error) {
	t.Helper()
	for k, v := range cloud.Environ() {
		t.Setenv(k, v)
	}
	var out, errOut strings.Builder
	err := runCLI(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upkeep.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneService = `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: R2_BUCKET
          value: demo-bucket
        - key: APP_ENV
          value: production
`

func TestValidateNeedsNoNetwork(t *testing.T) {
	// No fake at all: validate must not reach for a provider.
	var out, errOut strings.Builder
	path := writeConfig(t, oneService)
	if err := runCLI([]string{"validate", "-config", path}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 app(s), valid") {
		t.Errorf("got %q", out.String())
	}
}

func TestValidateRejectsACommittedCredential(t *testing.T) {
	path := writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: R2_SECRET_ACCESS_KEY
          value: SK1REALLOOKINGVALUE00
`)
	var out, errOut strings.Builder
	err := runCLI([]string{"validate", "-config", path}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("expected the config to be refused")
	}
	if !strings.Contains(err.Error(), "valueEnv") {
		t.Errorf("the error should name the fix, got %v", err)
	}
	if strings.Contains(err.Error(), "SK1REALLOOKINGVALUE00") {
		t.Error("the error printed the value it was refusing")
	}
}

func TestPlanReportsWhatIsMissing(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.Env["srv-000000000000000000"] = map[string]string{"APP_ENV": "production"}

	out, _, err := run(t, cloud, "", "plan", "-config", writeConfig(t, oneService))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CREATE") || !strings.Contains(out, "R2_BUCKET") {
		t.Errorf("expected the missing variable, got:\n%s", out)
	}
	if strings.Contains(out, "APP_ENV") {
		t.Errorf("a matching variable is not a change:\n%s", out)
	}
}

func TestPlanExitCodeSignalsDrift(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	_, _, err := run(t, cloud, "", "plan", "-exit-code", "-config", writeConfig(t, oneService))
	if err != errDrift {
		t.Fatalf("expected the drift signal, got %v", err)
	}

	// Converged: no signal.
	cloud.Env["srv-000000000000000000"] = map[string]string{
		"R2_BUCKET": "demo-bucket", "APP_ENV": "production",
	}
	if _, _, err := run(t, cloud, "", "plan", "-exit-code", "-config", writeConfig(t, oneService)); err != nil {
		t.Fatalf("a converged plan must not signal drift: %v", err)
	}
}

func TestApplyConvergesAndVerifies(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	out, _, err := run(t, cloud, "", "apply", "-config", writeConfig(t, oneService))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "verified") {
		t.Errorf("apply should re-read and say so:\n%s", out)
	}
	if cloud.Env["srv-000000000000000000"]["R2_BUCKET"] != "demo-bucket" {
		t.Errorf("the value never reached the service: %v", cloud.Env)
	}
}

// The failure the verification exists for, through the whole binary.
func TestApplyFailsWhenNothingActuallyChanges(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.DeafWrites = true

	out, _, err := run(t, cloud, "", "apply", "-config", writeConfig(t, oneService))
	if err == nil {
		t.Fatal("a write that changed nothing was reported as success")
	}
	if !strings.Contains(out, "APPLIED BUT DID NOT TAKE") {
		t.Errorf("the reader should be told plainly:\n%s", out)
	}
}

func TestASavedPlanIsAppliedOnlyIfTheWorldHeld(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	config := writeConfig(t, oneService)
	saved := filepath.Join(t.TempDir(), "reviewed.json")

	if _, _, err := run(t, cloud, "", "plan", "-config", config, "-out", saved); err != nil {
		t.Fatal(err)
	}

	// Somebody else set it in the meantime, so the reviewed CREATE is now
	// nothing at all.
	cloud.Env["srv-000000000000000000"] = map[string]string{
		"R2_BUCKET": "demo-bucket", "APP_ENV": "production",
	}

	_, _, err := run(t, cloud, "", "apply", "-config", config, saved)
	if err == nil {
		t.Fatal("the world moved; applying the old plan should be refused")
	}
	if !strings.Contains(err.Error(), "re-run plan") {
		t.Errorf("the error should say what to do, got %v", err)
	}
}

func TestASavedPlanAppliesWhenNothingMoved(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	config := writeConfig(t, oneService)
	saved := filepath.Join(t.TempDir(), "reviewed.json")

	if _, _, err := run(t, cloud, "", "plan", "-config", config, "-out", saved); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, cloud, "", "apply", "-config", config, saved); err != nil {
		t.Fatal(err)
	}
	if cloud.Env["srv-000000000000000000"]["R2_BUCKET"] != "demo-bucket" {
		t.Error("the saved plan did not apply")
	}
}

func TestSavedPlansAreNotWorldReadable(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	saved := filepath.Join(t.TempDir(), "reviewed.json")

	if _, _, err := run(t, cloud, "", "plan", "-config", writeConfig(t, oneService), "-out", saved); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("saved plan is %o, want 600", perm)
	}
}

func TestJsonPlanIsMachineReadable(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	out, _, err := run(t, cloud, "", "plan", "-json", "-config", writeConfig(t, oneService))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Changes int `json:"changes"`
		Actions []struct {
			Target string `json:"target"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got.Changes != 2 || len(got.Actions) != 2 {
		t.Errorf("expected two changes, got %+v", got)
	}
}

func TestConfigFromStdinComposesWithImport(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.Env["srv-000000000000000000"] = map[string]string{"APP_ENV": "production"}

	imported, _, err := run(t, cloud, "", "import", "-name", "demo", "-render", "srv-000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	piped := "version: 1\napps:\n" + imported

	out, _, err := run(t, cloud, piped, "plan", "-config", "-")
	if err != nil {
		t.Fatal(err)
	}
	// An imported config describes what is already there.
	if !strings.Contains(out, "no changes") {
		t.Errorf("an import should plan clean, got:\n%s", out)
	}
}

func TestImportNeverWritesALiveCredential(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.Env["srv-000000000000000000"] = map[string]string{
		"AUTH_SIGNING_KEY": "a-real-seed", "APP_ENV": "production",
	}

	out, _, err := run(t, cloud, "", "import", "-name", "demo", "-render", "srv-000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "a-real-seed") {
		t.Errorf("import wrote a live credential:\n%s", out)
	}
	if !strings.Contains(out, "valueEnv: AUTH_SIGNING_KEY") {
		t.Errorf("it should come back as a reference:\n%s", out)
	}
}

func TestDeployWaitsAndFailsOnABadBuild(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.DeployStatuses = []string{"build_failed"}

	_, _, err := run(t, cloud, "", "apply", "-deploy", "-config", writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      image: example/img:1
`))
	if err == nil {
		t.Fatal("a failed build must fail the apply")
	}
	if !strings.Contains(err.Error(), "build_failed") {
		t.Errorf("the error should name the status, got %v", err)
	}
	if cloud.Deploys != 1 {
		t.Errorf("expected one deploy, got %d", cloud.Deploys)
	}
}

func TestTheImageFlagOverridesTheConfig(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	_, _, err := run(t, cloud, "", "apply", "-deploy", "-image", "example/img:abc123", "-config", writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      image: example/img:latest
`))
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Deploys != 1 {
		t.Errorf("expected the deploy to run, got %d", cloud.Deploys)
	}
}

func TestAnUnknownCommandExplainsItself(t *testing.T) {
	var out, errOut strings.Builder
	err := runCLI([]string{"destroy"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("it should print usage, got %q", errOut.String())
	}
}

func TestVersionPrints(t *testing.T) {
	var out, errOut strings.Builder
	if err := runCLI([]string{"version"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Error("version printed nothing")
	}
}

// status answers "what is live right now" — the question a plan does not,
// because reading a list of changes and inferring the state behind it is not
// the same as being told the state.
func TestStatusReportsLiveState(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.Env["srv-000000000000000000"] = map[string]string{
		"APP_ENV": "production", "TZ": "UTC",
	}
	cloud.Buckets["demo-bucket"] = true
	cloud.PublicR2["demo-bucket"] = true

	out, _, err := run(t, cloud, "", "status", "-config", writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: APP_ENV
          value: production
        - key: R2_BUCKET
          value: demo-bucket
    r2:
      accountId: acct
      bucket: demo-bucket
      public: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 variable(s)") {
		t.Errorf("expected the variable count, got:\n%s", out)
	}
	// The one question a dashboard does not answer: which declared variables
	// are absent.
	if !strings.Contains(out, "1 not set: R2_BUCKET") {
		t.Errorf("expected the missing variable named, got:\n%s", out)
	}
	if !strings.Contains(out, "public") {
		t.Errorf("expected the bucket's access, got:\n%s", out)
	}
}

func TestStatusSaysWhenItCannotRead(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	t.Setenv("RENDER_API_KEY", "")
	t.Setenv("RENDER_API_URL", cloud.URL())
	// No render credential at all: absent is a state, and status must say which
	// surface it could not read rather than printing a confident blank.
	var out, errOut strings.Builder
	t.Setenv("HOME", t.TempDir())
	err := runCLI([]string{"status", "-config", writeConfig(t, oneService)},
		strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unreadable") {
		t.Errorf("expected it to say so, got:\n%s", out.String())
	}
}

func TestStatusJsonIsMachineReadable(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.Env["srv-000000000000000000"] = map[string]string{"APP_ENV": "production"}

	out, _, err := run(t, cloud, "", "status", "-json", "-config", writeConfig(t, oneService))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Apps []struct {
			Name     string `json:"name"`
			Surfaces []struct {
				Kind  string `json:"kind"`
				State string `json:"state"`
			} `json:"surfaces"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(got.Apps) != 1 || got.Apps[0].Name != "demo" {
		t.Fatalf("got %+v", got)
	}
	if got.Apps[0].Surfaces[0].Kind != "render" {
		t.Errorf("got %+v", got.Apps[0].Surfaces)
	}
}

// A gate that fails forever on something no tool can do is a gate people turn
// off, and then it catches nothing. Manual items get their own code so CI can
// choose.
func TestExitCodeSeparatesWhatCiCanFixFromWhatItCannot(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	manualOnly := writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: FEE_ACCOUNT_INFO
          manual: true
          why: real bank details
`)
	_, _, err := run(t, cloud, "", "plan", "-exit-code", "-config", manualOnly)
	if err != errManualOnly {
		t.Fatalf("manual-only should be its own signal, got %v", err)
	}

	// Something upkeep can actually do outranks it.
	_, _, err = run(t, cloud, "", "plan", "-exit-code", "-config", writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: R2_BUCKET
          value: demo-bucket
        - key: FEE_ACCOUNT_INFO
          manual: true
          why: real bank details
`))
	if err != errDrift {
		t.Fatalf("an executable change should signal drift, got %v", err)
	}

	// And once the manual item is set, the gate goes green.
	cloud.Env["srv-000000000000000000"] = map[string]string{"FEE_ACCOUNT_INFO": "a bank"}
	if _, _, err := run(t, cloud, "", "plan", "-exit-code", "-config", manualOnly); err != nil {
		t.Fatalf("nothing outstanding should exit clean, got %v", err)
	}
}

// Fly and Workers had planner tests but were never driven through the binary,
// unlike every other surface. These close that: a provider whose planner is
// right and whose wiring is wrong looks exactly like a provider that does
// nothing.
func TestFlySecretsThroughTheBinary(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	cloud.FlySecrets["EXISTING"] = true

	config := writeConfig(t, `
version: 1
apps:
  - name: demo
    fly:
      app: my-app
      secrets:
        - key: EXISTING
          value: x
        - key: MISSING
          value: y
`)
	out, _, err := run(t, cloud, "", "plan", "-config", config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "MISSING") {
		t.Errorf("expected the missing secret, got:\n%s", out)
	}
	// Fly never returns a value, so an existing secret must not be mentioned.
	if strings.Contains(out, "EXISTING") {
		t.Errorf("an existing secret cannot be compared, so it must not appear:\n%s", out)
	}

	if _, _, err := run(t, cloud, "", "apply", "-config", config); err != nil {
		t.Fatal(err)
	}
	if !cloud.FlySecrets["MISSING"] {
		t.Error("the secret never reached Fly")
	}
}

func TestWorkerRoutesThroughTheBinary(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()
	// A route that exists but is answered by another script: nothing looks
	// missing, and someone else's Worker is serving the path.
	cloud.WorkerRoutes["example.com/api/*"] = "an-older-worker"

	config := writeConfig(t, `
version: 1
apps:
  - name: demo
    workers:
      accountId: acct
      script: api
      zoneId: z1
      routes:
        - example.com/api/*
`)
	out, _, err := run(t, cloud, "", "plan", "-config", config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "an-older-worker") {
		t.Errorf("it should name who is answering, got:\n%s", out)
	}

	if _, _, err := run(t, cloud, "", "apply", "-config", config); err != nil {
		t.Fatal(err)
	}
	if cloud.WorkerRoutes["example.com/api/*"] != "api" {
		t.Errorf("the route was not repointed: %v", cloud.WorkerRoutes)
	}
}

// -app is a repeatable flag, and getting it wrong means silently reconciling
// more apps than were asked for.
func TestTheAppFlagNarrowsToWhatWasNamed(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	config := writeConfig(t, `
version: 1
apps:
  - name: one
    render:
      serviceId: srv-000000000000000000
      env:
        - key: FROM_ONE
          value: a
  - name: two
    render:
      serviceId: srv-111111111111111111
      env:
        - key: FROM_TWO
          value: b
`)
	out, _, err := run(t, cloud, "", "plan", "-app", "two", "-config", config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "FROM_ONE") {
		t.Errorf("an app that was not named must not be planned:\n%s", out)
	}
	if !strings.Contains(out, "FROM_TWO") {
		t.Errorf("the named app should be, got:\n%s", out)
	}

	// Repeated, it means both.
	both, _, err := run(t, cloud, "", "plan", "-app", "one", "-app", "two", "-config", config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(both, "FROM_ONE") || !strings.Contains(both, "FROM_TWO") {
		t.Errorf("expected both, got:\n%s", both)
	}
}

func TestTheReportCommandThroughTheBinary(t *testing.T) {
	cloud := testfake.New()
	defer cloud.Close()

	out, _, err := run(t, cloud, "", "report", "-config", writeConfig(t, `
version: 1
apps:
  - name: demo
    render:
      serviceId: srv-000000000000000000
      env:
        - key: R2_BUCKET
          value: b
        - key: FEE_ACCOUNT_INFO
          manual: true
          why: real bank details
`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 change(s) upkeep can make, 1 waiting on a human") {
		t.Errorf("headline wrong:\n%s", out)
	}
	canFix := strings.Index(out, "would close these")
	cannot := strings.Index(out, "No tool can do")
	if canFix < 0 || cannot < 0 || canFix > cannot {
		t.Errorf("what can be fixed should come first:\n%s", out)
	}
}

// The admin page fronts cloud credentials, so it never listens off-box unless
// told to in so many words.
func TestServeRefusesANonLoopbackAddress(t *testing.T) {
	var out, errOut strings.Builder
	err := runCLI([]string{"serve", "-addr", "0.0.0.0:1"}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("expected a refusal naming loopback, got %v", err)
	}
	if !strings.Contains(err.Error(), "-insecure") {
		t.Errorf("the error should name the override, got %v", err)
	}
	for _, ok := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if err := loopbackOnly(ok, false); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
	if err := loopbackOnly("0.0.0.0:1", true); err != nil {
		t.Errorf("-insecure should allow it: %v", err)
	}
}
