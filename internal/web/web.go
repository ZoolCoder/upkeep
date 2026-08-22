// Package web is the admin page: upkeep's commands behind a browser, on
// loopback, behind one password. It holds no state of its own beyond what a
// terminal session would — the last plan this process ran per app — because
// the providers are the state. Every action calls the same engine the CLI
// does and lands in the activity log.
//
// Files: web.go is the shell (auth, chrome, helpers); pages_*.go the sections.
package web

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/engine"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/zcadmin"
	"github.com/zoolcoder/zcadmin/auth"
)

//go:embed templates/*.html
var templateFS embed.FS

// activityLimit is how many rows the activity page shows.
const activityLimit = 200

// brand is the wordmark: up + keep.
var brand = zcadmin.Brand{Name: "up", Accent: "keep"}

// Credential is one provider's credential, as far as the page may say: whether
// it is there and where it came from. Never the value.
type Credential struct {
	Provider string // Render, Cloudflare, Neon, Fly
	Present  bool
	Source   string // the variable or the CLI session it was borrowed from
}

// Options is what the command line hands the page.
type Options struct {
	// ConfigPath is the YAML file, re-read on every request so an edit shows
	// without a restart.
	ConfigPath string
	// AuthFile holds the password hash; ActivityFile the JSONL log. Empty
	// means the XDG defaults for "upkeep".
	AuthFile, ActivityFile string
	// Providers builds the live clients, the same way the CLI does.
	Providers func() engine.Providers
	// Credentials reports presence per provider for Settings. Nil derives it
	// from Providers, without a source.
	Credentials func() []Credential
	// Getenv resolves ${VAR} in the config and valueEnv at apply time. Nil
	// means the process environment.
	Getenv func(string) string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// run is one plan or apply this process ran for an app. It is the only thing
// the page remembers between requests.
type run struct {
	At      time.Time
	Plan    plan.Plan
	Applied bool
	// Output is the apply transcript — what ran, what verified, what did not.
	Output string
	Err    error
}

// Server is the handler's state.
type Server struct {
	opts  Options
	owner string
	auth  *zcadmin.Auth
	log   *zcadmin.ActivityLog
	tmpl  *template.Template
	mux   *http.ServeMux

	mu   sync.Mutex
	runs map[string]run // app name → last run
}

// New builds the handler. The owner shown in the sidebar is the config's first
// app, or "upkeep" when the config cannot be read yet.
func New(opts Options) http.Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AuthFile == "" {
		opts.AuthFile = auth.DefaultFile("upkeep")
	}
	if opts.ActivityFile == "" {
		opts.ActivityFile = zcadmin.DefaultActivityFile("upkeep")
	}
	tmpl, err := zcadmin.Templates(templateFS, "templates/*.html", funcs)
	if err != nil {
		panic(err)
	}
	s := &Server{opts: opts, owner: "upkeep", tmpl: tmpl, mux: http.NewServeMux(), runs: map[string]run{}}
	if cfg, err := s.config(); err == nil && len(cfg.Apps) > 0 {
		s.owner = cfg.Apps[0].Name
	}
	s.log = &zcadmin.ActivityLog{Path: opts.ActivityFile}
	s.auth = zcadmin.NewAuth(brand, s.owner, auth.FileStore{Path: opts.AuthFile}, tmpl, opts.Now)
	s.auth.Log = func(detail string, ok bool) { s.record("auth", "", detail, ok) }
	s.auth.Routes(s.mux)
	s.mux.Handle("GET /static/", zcadmin.Static("/static/"))

	s.mux.HandleFunc("GET /{$}", s.dashboard)
	s.mux.HandleFunc("POST /plan", s.planAll)
	s.mux.HandleFunc("GET /apps", s.apps)
	s.mux.HandleFunc("GET /apps/{name}", s.appPage)
	s.mux.HandleFunc("POST /apps/{name}/plan", s.planApp)
	s.mux.HandleFunc("POST /apps/{name}/apply", s.applyApp)
	s.mux.HandleFunc("GET /activity", s.activity)
	s.mux.HandleFunc("GET /settings", s.settingsPage)
	s.mux.HandleFunc("POST /settings/password", s.settingsPassword)
	return s
}

// funcs are the extra template helpers beyond zcadmin.Funcs.
var funcs = template.FuncMap{
	"opChip": opChip,
}

// ServeHTTP puts zcadmin's login guard in front of every route.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.auth.Guard(s.mux).ServeHTTP(w, r)
}

// --- shared page chrome ----------------------------------------------------

type chrome = zcadmin.Chrome

func (s *Server) chrome(r *http.Request, active string) chrome {
	nav := []zcadmin.NavItem{
		{Href: "/", Label: "Dashboard", Key: "dashboard", Icon: zcadmin.Icons["grid"]},
		{Href: "/apps", Label: "Apps", Key: "apps", Icon: zcadmin.Icons["cloud"]},
		{Href: "/activity", Label: "Activity", Key: "activity", Icon: zcadmin.Icons["pulse"]},
		{Href: "/settings", Label: "Settings", Key: "settings", Icon: zcadmin.Icons["gear"]},
	}
	return zcadmin.NewChrome(r, brand, s.owner, nav, active)
}

// --- config, providers, runs -----------------------------------------------

func (s *Server) config() (config.Config, error) {
	return config.Load(s.opts.ConfigPath, s.opts.Getenv)
}

func (s *Server) providers() engine.Providers {
	if s.opts.Providers == nil {
		return engine.Providers{}
	}
	return s.opts.Providers()
}

// credentials is Settings' view: what the command line found, or — without a
// reporter — whether each provider has a client at all.
func (s *Server) credentials() []Credential {
	if s.opts.Credentials != nil {
		return s.opts.Credentials()
	}
	p := s.providers()
	return []Credential{
		{Provider: "Render", Present: p.Render != nil},
		{Provider: "Cloudflare", Present: p.CF != nil},
		{Provider: "Neon", Present: p.Neon != nil},
		{Provider: "Fly", Present: p.Fly != nil},
	}
}

// engineFor is one engine per app, built the way the CLI builds its one:
// same providers, same environment, same deploy switch.
func (s *Server) engineFor(cfg config.Config, app string, deploy bool) *engine.Engine {
	return engine.New(cfg, s.providers(), engine.Options{
		Apps: []string{app}, Deploy: deploy, Getenv: s.opts.Getenv,
	})
}

// plan runs a plan for one app and remembers it.
func (s *Server) plan(ctx context.Context, cfg config.Config, app string) (plan.Plan, error) {
	p, err := s.engineFor(cfg, app, false).Plan(ctx)
	s.remember(app, run{At: s.opts.Now(), Plan: p, Err: err})
	return p, err
}

// apply is the CLI's apply: plan, run it, and let the engine re-read to verify.
// The transcript is kept so the page can show what held and what did not, and
// the plan is re-read once more so the table reflects the world after.
func (s *Server) apply(ctx context.Context, cfg config.Config, app string, deploy bool) run {
	eng := s.engineFor(cfg, app, deploy)
	p, err := eng.Plan(ctx)
	if err != nil {
		r := run{At: s.opts.Now(), Plan: p, Applied: true, Err: err}
		s.remember(app, r)
		return r
	}
	var out bytes.Buffer
	_ = p.Write(&out)
	out.WriteString("\n")
	applyErr := eng.Apply(ctx, p, &out)
	after, err := s.engineFor(cfg, app, false).Plan(ctx)
	if err != nil {
		after = p
	}
	r := run{At: s.opts.Now(), Plan: after, Applied: true, Output: out.String(), Err: applyErr}
	s.remember(app, r)
	return r
}

func (s *Server) remember(app string, r run) {
	s.mu.Lock()
	s.runs[app] = r
	s.mu.Unlock()
}

func (s *Server) lastRun(app string) (run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[app]
	return r, ok
}

// --- helpers ---------------------------------------------------------------

func (s *Server) record(kind, target, detail string, ok bool) {
	_ = s.log.Append(zcadmin.Activity{At: s.opts.Now(), Kind: kind, Target: target, Detail: detail, OK: ok})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	zcadmin.Render(w, s.tmpl, name, data)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *Server) ago(t time.Time) string { return zcadmin.Ago(t, s.opts.Now()) }

// findApp picks one app out of the config by name.
func findApp(cfg config.Config, name string) (config.App, bool) {
	for _, a := range cfg.Apps {
		if a.Name == name {
			return a, true
		}
	}
	return config.App{}, false
}

// actionRow is one plan line for a table: the CLI's columns plus a chip class.
type actionRow struct {
	Op       string
	Resource string
	Target   string
	Detail   string
}

// rows renders a plan in the CLI's order: by resource, then target.
func rows(p plan.Plan) []actionRow {
	out := make([]actionRow, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, actionRow{Op: string(a.Op), Resource: a.Resource, Target: a.Target, Detail: a.Detail})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// opChip maps an op to a chip colour. The meanings are fixed: teal = will be
// made, amber = will be changed, coral = will be deleted, violet = a
// decision only a person can make.
func opChip(op string) string {
	switch plan.Op(op) {
	case plan.OpCreate:
		return "on"
	case plan.OpUpdate:
		return "warn"
	case plan.OpDelete:
		return "bad"
	case plan.OpManual:
		return "violet"
	}
	return "plain"
}

// counts tallies a plan by op.
type counts struct {
	Create, Update, Delete, Manual int
}

func count(p plan.Plan) counts {
	var c counts
	for _, a := range p.Actions {
		switch a.Op {
		case plan.OpCreate:
			c.Create++
		case plan.OpUpdate:
			c.Update++
		case plan.OpDelete:
			c.Delete++
		case plan.OpManual:
			c.Manual++
		}
	}
	return c
}

// summary is the one line a card says about a plan.
func summary(p plan.Plan) string {
	if p.Empty() {
		return "matches the config"
	}
	c := count(p)
	var parts []string
	add := func(n int, word string) {
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+word)
		}
	}
	add(c.Create, "to create")
	add(c.Update, "to update")
	add(c.Delete, "to delete")
	add(c.Manual, "manual")
	return strings.Join(parts, ", ")
}
