// Package engine turns a config plus live provider state into an ordered plan,
// and executes it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/zoolcoder/upkeep/internal/authcheck"
	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/dns"
	"github.com/zoolcoder/upkeep/internal/flyapi"
	"github.com/zoolcoder/upkeep/internal/flysecrets"
	"github.com/zoolcoder/upkeep/internal/neon"
	"github.com/zoolcoder/upkeep/internal/neonapi"
	"github.com/zoolcoder/upkeep/internal/pages"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/r2"
	"github.com/zoolcoder/upkeep/internal/renderapi"
	"github.com/zoolcoder/upkeep/internal/renderenv"
	"github.com/zoolcoder/upkeep/internal/secret"
	"github.com/zoolcoder/upkeep/internal/workers"
)

// Render is everything the engine asks of Render: read an environment, write
// one variable, ship a deploy.
type Render interface {
	EnvVars(ctx context.Context, serviceID string) (map[string]string, error)
	SetEnvVar(ctx context.Context, serviceID, key, value string) error
	Deploy(ctx context.Context, serviceID, image string) (string, error)
	DeployStatus(ctx context.Context, serviceID, deployID string) (renderapi.DeployStatus, error)
}

// Fly is the second hosting provider. It shares no code with Render — only
// this shape — which is what makes the seam a seam.
type Fly interface {
	Secrets(ctx context.Context, app string) (map[string]flyapi.Secret, error)
	SetSecret(ctx context.Context, app, name, value string) error
}

// CF is the Cloudflare transport, shared by the R2 and Pages planners.
type CF interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

// Neon is the read half of the database provider.
type Neon = neon.API

// Providers are the live systems. Any may be nil: an app that declares a
// surface with no provider behind it gets one manual action saying which
// credential is missing, rather than a crash halfway through a plan.
//
// Interfaces, not clients, so the engine's own verify path can be tested
// against a provider that accepts every write and changes nothing — which is
// the failure it exists to catch, and which no real API will perform on demand.
type Providers struct {
	Render Render
	CF     CF
	Neon   Neon
	Fly    Fly
}

var (
	_ Render = (*renderapi.Client)(nil)
	_ CF     = (*cfapi.Client)(nil)
	_ Neon   = (*neonapi.Client)(nil)
	_ Fly    = (*flyapi.Client)(nil)
)

type Options struct {
	// Apps limits the run to these names. Empty means every app.
	Apps []string
	// Deploy also triggers a Render deploy after the environment converges.
	Deploy bool
	// Getenv resolves valueEnv references. Nil means os.Getenv.
	Getenv func(string) string
	// DeployTimeout bounds the wait for a triggered deploy. Zero means the
	// default; a deploy that is still building after this long is reported as
	// unfinished rather than failed, because it may yet succeed.
	DeployTimeout time.Duration
	// PollEvery is how often the deploy is checked. Zero means the default.
	PollEvery time.Duration
	// Sleep is injected so tests do not wait. Nil means time.Sleep.
	Sleep func(time.Duration)
	// Secrets resolves valueFrom references. Nil means the default set.
	Secrets *secret.Set
	// Fetch reads the URLs the auth checks probe. Nil means a real client.
	Fetch authcheck.Fetcher
	// Concurrency bounds how many apps are read at once. Zero means the
	// default; one makes a run strictly sequential.
	//
	// Only READING is parallel. Applying stays in order, because the order is
	// load-bearing — a bucket is created before its settings are written — and
	// because a failure part-way through a sequential apply is re-runnable
	// while a failure part-way through a parallel one is a puzzle.
	Concurrency int
}

const defaultConcurrency = 8

const (
	defaultDeployTimeout = 20 * time.Minute
	defaultPollEvery     = 15 * time.Second
)

type Engine struct {
	cfg  config.Config
	prov Providers
	opts Options

	mu    sync.Mutex
	envOf map[string]map[string]string // service id → live env, read once
}

func New(cfg config.Config, prov Providers, opts Options) *Engine {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Secrets == nil {
		opts.Secrets = secret.Default()
	}
	return &Engine{cfg: cfg, prov: prov, opts: opts, envOf: map[string]map[string]string{}}
}

// Plan reads live state and returns everything that would change. It performs
// no writes.
func (e *Engine) Plan(ctx context.Context) (plan.Plan, error) {
	apps, err := e.selected()
	if err != nil {
		return plan.Plan{}, err
	}
	// Read every app at once, up to the limit, but keep the results in config
	// order: a plan whose lines move between runs cannot be diffed, and diffing
	// two runs is most of what a plan is for.
	limit := e.opts.Concurrency
	if limit < 1 {
		limit = defaultConcurrency
	}

	plans := make([]plan.Plan, len(apps))
	errs := make([]error, len(apps))
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, app := range apps {
		wg.Add(1)
		go func(i int, app config.App) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			p, err := e.planApp(ctx, app)
			if err != nil {
				errs[i] = fmt.Errorf("app %s: %w", app.Name, err)
				return
			}
			plans[i] = p
		}(i, app)
	}
	wg.Wait()

	// The first failure in config order, so the same broken config always
	// reports the same thing.
	for _, err := range errs {
		if err != nil {
			return plan.Plan{}, err
		}
	}
	var out plan.Plan
	for _, p := range plans {
		out.Extend(p)
	}
	return out, nil
}

func (e *Engine) planApp(ctx context.Context, app config.App) (plan.Plan, error) {
	var out plan.Plan

	if r := app.Render; r != nil {
		if e.prov.Render == nil {
			out.Add(missingCredential("render-env", r.ServiceID, "RENDER_API_KEY, or `render login`"))
		} else {
			p, err := renderenv.Plan(ctx, e.prov.Render, *r, e.opts.Getenv, e.opts.Secrets)
			if err != nil {
				return out, err
			}
			out.Extend(p)
			if e.opts.Deploy {
				out.Add(e.deployAction(e.prov.Render, *r))
			}
		}
	}

	if w := app.Workers; w != nil {
		if e.prov.CF == nil {
			out.Add(missingCredential("worker-secret", w.Script, "CLOUDFLARE_API_TOKEN"))
		} else {
			p, err := workers.Plan(ctx, e.prov.CF, *w, e.opts.Getenv, e.opts.Secrets)
			if err != nil {
				return out, err
			}
			out.Extend(p)
		}
	}

	if f := app.Fly; f != nil {
		if e.prov.Fly == nil {
			out.Add(missingCredential("fly-secret", f.App, "FLY_API_TOKEN, or `fly auth login`"))
		} else {
			p, err := flysecrets.Plan(ctx, e.prov.Fly, *f, e.opts.Getenv, e.opts.Secrets)
			if err != nil {
				return out, err
			}
			out.Extend(p)
		}
	}

	if b := app.R2; b != nil {
		if e.prov.CF == nil {
			out.Add(missingCredential("r2-bucket", b.Bucket, "CLOUDFLARE_API_TOKEN"))
		} else {
			p, err := r2.Plan(ctx, e.prov.CF, *b)
			if err != nil {
				return out, err
			}
			out.Extend(p)
		}
	}

	if p := app.Pages; p != nil {
		if e.prov.CF == nil {
			out.Add(missingCredential("pages", p.Project, "CLOUDFLARE_API_TOKEN"))
		} else {
			pp, err := pages.Plan(ctx, e.prov.CF, *p)
			if err != nil {
				return out, err
			}
			out.Extend(pp)
		}
	}

	// Auth reads the service's environment and compares its parts, so it needs
	// the same environment the render surface read.
	if a := app.Auth; a != nil {
		switch {
		case app.Render == nil:
			out.Add(plan.Action{
				Op: plan.OpManual, Resource: "auth", Target: app.Name,
				Detail: "an auth block needs a render service to read its variables from",
			})
		case e.prov.Render == nil:
			out.Add(missingCredential("auth", app.Name, "RENDER_API_KEY, or `render login`"))
		default:
			env, err := e.liveEnv(ctx, app.Render.ServiceID)
			if err != nil {
				return out, err
			}
			ap, err := authcheck.Plan(ctx, *a, env, e.fetcher())
			if err != nil {
				return out, err
			}
			out.Extend(ap)
		}
	}

	if d := app.DNS; d != nil {
		if e.prov.CF == nil {
			out.Add(missingCredential("dns", d.ZoneID, "CLOUDFLARE_API_TOKEN"))
		} else {
			dp, err := dns.Plan(ctx, e.prov.CF, *d)
			if err != nil {
				return out, err
			}
			out.Extend(dp)
		}
	}

	if n := app.Neon; n != nil {
		if e.prov.Neon == nil {
			out.Add(missingCredential("neon-branch", n.ProjectID, "NEON_API_KEY"))
		} else {
			live := ""
			if app.Render != nil && e.prov.Render != nil {
				env, err := e.liveEnv(ctx, app.Render.ServiceID)
				if err != nil {
					return out, err
				}
				live = env["DATABASE_URL"]
			}
			np, err := neon.Plan(ctx, e.prov.Neon, *n, live, e.opts.Getenv)
			if err != nil {
				return out, err
			}
			out.Extend(np)
		}
	}
	return out, nil
}

// liveEnv reads a service's environment once per run. Guarded because apps are
// planned concurrently and two of them may share a service.
func (e *Engine) liveEnv(ctx context.Context, serviceID string) (map[string]string, error) {
	e.mu.Lock()
	env, ok := e.envOf[serviceID]
	e.mu.Unlock()
	if ok {
		return env, nil
	}

	env, err := e.prov.Render.EnvVars(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.envOf[serviceID] = env
	e.mu.Unlock()
	return env, nil
}

func (e *Engine) deployAction(api Render, r config.Render) plan.Action {
	detail := "trigger a deploy and wait for it"
	if r.Image != "" {
		detail = "deploy " + r.Image + " and wait for it"
	}
	return plan.Action{
		Op: plan.OpUpdate, Resource: "render-deploy", Target: r.ServiceID, Detail: detail,
		Do: func(ctx context.Context) error {
			id, err := api.Deploy(ctx, r.ServiceID, r.Image)
			if err != nil {
				return err
			}
			return e.awaitDeploy(ctx, api, r.ServiceID, id)
		},
	}
}

// awaitDeploy waits for a triggered deploy to finish.
//
// Triggering is not deploying. Render accepts the request and returns an id
// long before it knows whether the image builds, so a tool that stopped here
// would report a successful deploy of code that never started — the exact
// shape of failure everything else in upkeep is built to refuse.
func (e *Engine) awaitDeploy(ctx context.Context, api Render, serviceID, deployID string) error {
	timeout, every, sleep := e.deployTiming()
	deadline := time.Now().Add(timeout)

	for {
		status, err := api.DeployStatus(ctx, serviceID, deployID)
		if err != nil {
			return fmt.Errorf("deploy %s: reading status: %w", deployID, err)
		}
		switch {
		case status.Live():
			return nil
		case status.Failed():
			return fmt.Errorf("deploy %s ended %s", deployID, status.Status)
		}
		if time.Now().After(deadline) {
			// Not a failure: it may still succeed. Saying which it is beats
			// guessing on the caller's behalf.
			return fmt.Errorf(
				"deploy %s was still %s after %s — it may yet finish; check the dashboard",
				deployID, status.Status, timeout)
		}
		sleep(every)
	}
}

func (e *Engine) deployTiming() (timeout, every time.Duration, sleep func(time.Duration)) {
	timeout, every, sleep = e.opts.DeployTimeout, e.opts.PollEvery, e.opts.Sleep
	if timeout == 0 {
		timeout = defaultDeployTimeout
	}
	if every == 0 {
		every = defaultPollEvery
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return timeout, every, sleep
}

func missingCredential(resource, target, needs string) plan.Action {
	return plan.Action{
		Op: plan.OpManual, Resource: resource, Target: target,
		Detail: "no credential for this provider; set " + needs,
	}
}

// Apply runs every executable action in order, stopping at the first failure so
// a half-applied plan is re-runnable rather than compounded.
//
// Manual actions are written to w and never executed: a caller that ignores
// them is reporting a half-built deployment as a finished one.
//
// It then re-reads. A provider returning 200 is not evidence the change took —
// a value can be accepted and normalised, a setting can be write-through to
// something that rejects it later, and an API can simply lie. Every failure
// this tool was built after looked like success at the moment it happened.
func (e *Engine) Apply(ctx context.Context, p plan.Plan, w io.Writer) error {
	if err := e.apply(ctx, p, w); err != nil {
		return err
	}
	if len(p.Executable()) == 0 {
		return nil
	}
	return e.verify(ctx, w)
}

// verify re-plans and reports anything the apply did not actually converge.
//
// Manual actions are expected to survive — they are what nobody can automate —
// so only executable leftovers count as a failure to converge.
//
// The deploy is excluded. It is a command, not a state: re-planning always
// lists it, so leaving it in would make every -deploy run report that its own
// deploy did not take.
func (e *Engine) verify(ctx context.Context, w io.Writer) error {
	e.mu.Lock()
	e.envOf = map[string]map[string]string{}
	e.mu.Unlock()
	deploy := e.opts.Deploy
	e.opts.Deploy = false
	defer func() { e.opts.Deploy = deploy }()

	after, err := e.Plan(ctx)
	if err != nil {
		return fmt.Errorf("applied, but re-reading to check failed: %w", err)
	}
	left := after.Executable()
	if len(left) == 0 {
		fmt.Fprintln(w, "\nverified: re-read the live state and it matches the config")
		return nil
	}
	fmt.Fprintln(w, "\nAPPLIED BUT DID NOT TAKE:")
	for _, a := range left {
		fmt.Fprintf(w, "  %s %s %s — %s\n", a.Op, a.Resource, a.Target, a.Detail)
	}
	return fmt.Errorf("%d change(s) reported success and are still not in place", len(left))
}

func (e *Engine) apply(ctx context.Context, p plan.Plan, w io.Writer) error {
	for _, action := range p.Executable() {
		fmt.Fprintf(w, "%s %s %s… ", action.Op, action.Resource, action.Target)
		if action.Do == nil {
			return errors.New("action has no Do but is not manual — this is a bug in upkeep")
		}
		if err := action.Do(ctx); err != nil {
			fmt.Fprintln(w, "failed")
			return fmt.Errorf("%s %s %s: %w", action.Op, action.Resource, action.Target, err)
		}
		fmt.Fprintln(w, "done")
	}
	if manual := p.Manual(); len(manual) > 0 {
		fmt.Fprintf(w, "\nStill yours to do:\n")
		for _, a := range manual {
			fmt.Fprintf(w, "  %s %s — %s\n", a.Resource, a.Target, a.Detail)
		}
	}
	return nil
}

func (e *Engine) selected() ([]config.App, error) {
	return Select(e.cfg, e.opts.Apps)
}

// Select narrows a config to the named apps, in the order they were named.
// Exported because status answers a different question about the same subset.
func Select(cfg config.Config, names []string) ([]config.App, error) {
	if len(names) == 0 {
		return cfg.Apps, nil
	}
	byName := map[string]config.App{}
	for _, a := range cfg.Apps {
		byName[a.Name] = a
	}
	out := make([]config.App, 0, len(names))
	for _, name := range names {
		app, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("no app named %q in the config", name)
		}
		out = append(out, app)
	}
	return out, nil
}

func (e *Engine) fetcher() authcheck.Fetcher {
	if e.opts.Fetch != nil {
		return e.opts.Fetch
	}
	return authcheck.HTTP{}
}
