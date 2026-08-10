package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/neonapi"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/renderapi"
	"github.com/zoolcoder/upkeep/internal/renderenv"
	"github.com/zoolcoder/upkeep/internal/secret"
)

func renderPlan(ctx context.Context, api renderenv.Setter, r *config.Render) (plan.Plan, error) {
	return renderenv.Plan(ctx, api, *r, func(string) string { return "" }, secret.Default())
}

// The whole point, in one test: with no credentials for any provider, a run
// still tells the operator exactly which ones are missing.
//
// The alternative — refusing to run without every credential — is how a tool
// gets used once and then bypassed.
func TestNoCredentialsStillProducesAUsefulPlan(t *testing.T) {
	cfg := config.Config{Version: 1, Apps: []config.App{{
		Name:   "zoolaqar",
		Render: &config.Render{ServiceID: "srv-1"},
		R2:     &config.R2{AccountID: "acct", Bucket: "zoolaqar"},
		Pages:  &config.Pages{AccountID: "acct", Project: "zoolaqar"},
		Neon:   &config.Neon{ProjectID: "proj"},
	}}}

	p, err := New(cfg, Providers{}, Options{Getenv: func(string) string { return "" }}).
		Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(p.Executable()) != 0 {
		t.Error("nothing is executable without a credential")
	}
	var rendered strings.Builder
	if err := p.Write(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RENDER_API_KEY", "CLOUDFLARE_API_TOKEN", "NEON_API_KEY"} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("the plan should name %s, got:\n%s", want, rendered.String())
		}
	}
}

func TestAnUnknownAppIsAnError(t *testing.T) {
	cfg := config.Config{Version: 1, Apps: []config.App{{
		Name: "zoolaqar", R2: &config.R2{AccountID: "a", Bucket: "b"},
	}}}
	_, err := New(cfg, Providers{}, Options{Apps: []string{"nope"}}).Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}

// A plan that lists only manual actions has converged as far as software can
// take it. Apply must say so rather than reporting success on a half-built
// deployment.
func TestApplyReportsWhatItCannotDo(t *testing.T) {
	p := plan.Plan{Actions: []plan.Action{{
		Op: plan.OpManual, Resource: "render-env", Target: "FEE_ACCOUNT_INFO",
		Detail: "real bank details, shown to a seeker verbatim",
	}}}

	var out strings.Builder
	eng := New(config.Config{Version: 1}, Providers{}, Options{})
	if err := eng.Apply(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Still yours to do") {
		t.Errorf("apply must surface manual actions, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FEE_ACCOUNT_INFO") {
		t.Errorf("it must name them, got:\n%s", out.String())
	}
}

// A failure part-way leaves the rest unapplied, so a re-run is a re-run and not
// a compounding mess.
func TestApplyStopsAtTheFirstFailure(t *testing.T) {
	ran := 0
	fail := func(context.Context) error { ran++; return context.Canceled }
	count := func(context.Context) error { ran++; return nil }

	p := plan.Plan{Actions: []plan.Action{
		{Op: plan.OpCreate, Resource: "render-env", Target: "A", Do: count},
		{Op: plan.OpCreate, Resource: "render-env", Target: "B", Do: fail},
		{Op: plan.OpCreate, Resource: "render-env", Target: "C", Do: count},
	}}

	var out strings.Builder
	err := New(config.Config{Version: 1}, Providers{}, Options{}).
		Apply(context.Background(), p, &out)
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	if ran != 2 {
		t.Errorf("expected to stop after the failure, ran %d actions", ran)
	}
	if !strings.Contains(err.Error(), "B") {
		t.Errorf("the error should name what failed, got %v", err)
	}
}

// A provider returning 200 is not evidence a change took. Every failure this
// tool was built after looked like success at the moment it happened.
type stubRender struct {
	live map[string]string
	// deaf accepts every write and changes nothing, which is what a provider
	// that normalises, defers, or simply lies looks like from here.
	deaf     bool
	deployed int
	polls    int
	statuses []string
}

func (s *stubRender) EnvVars(context.Context, string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range s.live {
		out[k] = v
	}
	return out, nil
}

func (s *stubRender) SetEnvVar(_ context.Context, _, key, value string) error {
	if s.deaf {
		return nil
	}
	s.live[key] = value
	return nil
}

func (s *stubRender) Deploy(context.Context, string, string) (string, error) {
	s.deployed++
	return "dep-1", nil
}

// statuses is walked one per poll, so a test can say "building, building, live".
func (s *stubRender) DeployStatus(context.Context, string, string) (renderapi.DeployStatus, error) {
	s.polls++
	if len(s.statuses) == 0 {
		return renderapi.DeployStatus{ID: "dep-1", Status: "live"}, nil
	}
	next := s.statuses[0]
	if len(s.statuses) > 1 {
		s.statuses = s.statuses[1:]
	}
	return renderapi.DeployStatus{ID: "dep-1", Status: next}, nil
}

func engineOver(r *stubRender, env []config.EnvVar) *Engine {
	return New(config.Config{Version: 1, Apps: []config.App{{
		Name: "app", Render: &config.Render{ServiceID: "srv-1", Env: env},
	}}}, Providers{Render: r}, Options{Getenv: func(string) string { return "" }})
}

func TestApplyVerifiesTheChangeTook(t *testing.T) {
	live := &stubRender{live: map[string]string{}}
	eng := engineOver(live, []config.EnvVar{{Key: "R2_BUCKET", Value: "zoolaqar"}})

	p, err := eng.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := eng.Apply(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verified") {
		t.Errorf("apply should say it re-read, got:\n%s", out.String())
	}
	if live.live["R2_BUCKET"] != "zoolaqar" {
		t.Error("the value did not reach the provider")
	}
}

// The case that matters: every write is accepted and nothing changes. Without
// the re-read this reports a clean success on a deployment that is still wrong.
func TestADeafProviderIsCaughtNotBelieved(t *testing.T) {
	live := &stubRender{live: map[string]string{}, deaf: true}
	eng := engineOver(live, []config.EnvVar{{Key: "R2_BUCKET", Value: "zoolaqar"}})

	p, err := eng.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err = eng.Apply(context.Background(), p, &out)
	if err == nil {
		t.Fatal("a write that changed nothing was reported as success")
	}
	if !strings.Contains(out.String(), "APPLIED BUT DID NOT TAKE") {
		t.Errorf("the reader should be told plainly, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "R2_BUCKET") {
		t.Errorf("it should name what did not take, got:\n%s", out.String())
	}
}

// A converged run must not claim to have verified something it never touched.
func TestNothingToApplyDoesNotClaimVerification(t *testing.T) {
	live := &stubRender{live: map[string]string{"R2_BUCKET": "zoolaqar"}}
	eng := engineOver(live, []config.EnvVar{{Key: "R2_BUCKET", Value: "zoolaqar"}})

	p, err := eng.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := eng.Apply(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "verified") {
		t.Errorf("nothing ran, so there is nothing to verify, got:\n%s", out.String())
	}
}

func TestDeployIsPlannedOnlyWhenAsked(t *testing.T) {
	live := &stubRender{live: map[string]string{}}
	eng := New(config.Config{Version: 1, Apps: []config.App{{
		Name: "app", Render: &config.Render{ServiceID: "srv-1", Image: "img:1"},
	}}}, Providers{Render: live}, Options{Deploy: true, Getenv: func(string) string { return "" }})

	p, err := eng.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := eng.Apply(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	if live.deployed != 1 {
		t.Errorf("expected one deploy, got %d", live.deployed)
	}
}

// deployEngine never really sleeps, so a test that walks several polls is
// instant.
func deployEngine(r *stubRender) *Engine {
	return New(config.Config{Version: 1, Apps: []config.App{{
		Name: "app", Render: &config.Render{ServiceID: "srv-1", Image: "img:1"},
	}}}, Providers{Render: r}, Options{
		Deploy: true, Getenv: func(string) string { return "" },
		PollEvery: time.Millisecond, Sleep: func(time.Duration) {},
	})
}

func runDeploy(t *testing.T, eng *Engine) error {
	t.Helper()
	p, err := eng.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	return eng.Apply(context.Background(), p, &out)
}

// Triggering is not deploying. Render returns an id long before it knows
// whether the image builds.
func TestADeployIsWaitedFor(t *testing.T) {
	live := &stubRender{live: map[string]string{}, statuses: []string{"build_in_progress", "update_in_progress", "live"}}

	if err := runDeploy(t, deployEngine(live)); err != nil {
		t.Fatal(err)
	}
	if live.polls < 3 {
		t.Errorf("expected to keep checking, polled %d times", live.polls)
	}
}

// A deploy that never built must not read as a successful one.
func TestAFailedDeployFails(t *testing.T) {
	for _, status := range []string{"build_failed", "update_failed", "pre_deploy_failed", "canceled"} {
		live := &stubRender{live: map[string]string{}, statuses: []string{status}}
		err := runDeploy(t, deployEngine(live))
		if err == nil {
			t.Errorf("%s should fail the apply", status)
			continue
		}
		if !strings.Contains(err.Error(), status) {
			t.Errorf("the error should name the status, got %v", err)
		}
	}
}

// Still building after the timeout is not the same as failed, and saying which
// beats guessing on the caller's behalf.
func TestAnUnfinishedDeploySaysSoWithoutCallingItFailed(t *testing.T) {
	live := &stubRender{live: map[string]string{}, statuses: []string{"build_in_progress"}}
	eng := deployEngine(live)
	eng.opts.DeployTimeout = time.Nanosecond

	err := runDeploy(t, eng)
	if err == nil {
		t.Fatal("expected the wait to give up")
	}
	if !strings.Contains(err.Error(), "may yet finish") {
		t.Errorf("it should not claim failure, got %v", err)
	}
}

// Apps are read concurrently, so two runs of one config must still produce the
// same plan in the same order: a plan whose lines move cannot be diffed, and
// diffing two runs is most of what a plan is for.
func TestConcurrentReadsStillProduceAStableOrder(t *testing.T) {
	apps := make([]config.App, 0, 8)
	for _, name := range []string{"h", "b", "f", "a", "g", "c", "e", "d"} {
		apps = append(apps, config.App{
			Name: name,
			Render: &config.Render{
				ServiceID: "srv-" + name,
				Env:       []config.EnvVar{{Key: "K_" + name, Value: "v"}},
			},
		})
	}
	cfg := config.Config{Version: 1, Apps: apps}

	var first string
	for run := 0; run < 5; run++ {
		eng := New(cfg, Providers{Render: &stubRender{live: map[string]string{}}},
			Options{Getenv: func(string) string { return "" }})
		p, err := eng.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := p.Write(&out); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = out.String()
			continue
		}
		if out.String() != first {
			t.Fatalf("run %d differs:\n%s\nfirst:\n%s", run, out.String(), first)
		}
	}
	if !strings.Contains(first, "K_a") || !strings.Contains(first, "K_h") {
		t.Errorf("every app should be planned:\n%s", first)
	}
}

// One broken app in a concurrent read must report the same failure every time,
// not whichever goroutine happened to lose first.
func TestTheFirstFailureInConfigOrderWins(t *testing.T) {
	cfg := config.Config{Version: 1, Apps: []config.App{
		{Name: "one", Neon: &config.Neon{ProjectID: "p1", Branch: "main"}},
		{Name: "two", Neon: &config.Neon{ProjectID: "p2", Branch: "main"}},
	}}
	for i := 0; i < 5; i++ {
		eng := New(cfg, Providers{Neon: failingNeon{}}, Options{})
		_, err := eng.Plan(context.Background())
		if err == nil {
			t.Fatal("expected a failure")
		}
		if !strings.Contains(err.Error(), "app one") {
			t.Fatalf("expected the first app's error, got %v", err)
		}
	}
}

type failingNeon struct{}

func (failingNeon) Branches(context.Context, string) ([]neonapi.Branch, error) {
	return nil, context.DeadlineExceeded
}
