package web

// Apps: the roster, and one page per app with its live status, the plan
// table, and the two buttons the CLI has — Plan, and Apply behind a typed
// confirmation.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/engine"
	"github.com/zoolcoder/upkeep/internal/status"
	"github.com/zoolcoder/zcadmin"
)

type appsPage struct {
	chrome
	ConfigErr string
	Apps      []appCard
}

func (s *Server) apps(w http.ResponseWriter, r *http.Request) {
	p := appsPage{chrome: s.chrome(r, "apps")}
	cfg, err := s.config()
	if err != nil {
		p.ConfigErr = err.Error()
	}
	for _, app := range cfg.Apps {
		p.Apps = append(p.Apps, s.card(app.Name, surfaceNames(app)))
	}
	s.render(w, "apps.html", p)
}

type appPage struct {
	chrome
	Name     string
	Surfaces []string
	Status   []status.Surface
	// HasRun is whether this process has planned or applied the app yet.
	HasRun  bool
	Planned string
	Applied bool
	Summary string
	Counts  counts
	Rows    []actionRow
	Output  string
	RunErr  string
	// CanDeploy is whether the config names a Render service, which is what
	// -deploy acts on.
	CanDeploy bool
}

func (s *Server) appPage(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.config()
	if err != nil {
		s.fail(w, err)
		return
	}
	app, ok := findApp(cfg, r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := appPage{chrome: s.chrome(r, "apps"), Name: app.Name, Surfaces: surfaceNames(app), CanDeploy: app.Render != nil}
	prov := s.providers()
	live := status.Read(r.Context(), []config.App{app}, statusRender(prov), statusCF(prov))
	if len(live) == 1 {
		p.Status = live[0].Surfaces
	}
	if last, ok := s.lastRun(app.Name); ok {
		p.HasRun = true
		p.Planned = s.ago(last.At)
		p.Applied = last.Applied
		p.Summary = summary(last.Plan)
		p.Counts = count(last.Plan)
		p.Rows = rows(last.Plan)
		p.Output = last.Output
		if last.Err != nil {
			p.RunErr = last.Err.Error()
		}
	}
	s.render(w, "app.html", p)
}

func (s *Server) planApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	back := "/apps/" + name
	cfg, err := s.config()
	if err != nil {
		zcadmin.Back(w, r, back, "", err)
		return
	}
	if _, ok := findApp(cfg, name); !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.plan(r.Context(), cfg, name)
	if err != nil {
		s.record("plan", name, "plan failed: "+err.Error(), false)
		zcadmin.Back(w, r, back, "", err)
		return
	}
	s.record("plan", name, "planned: "+summary(p), true)
	zcadmin.Back(w, r, back, "planned "+name+": "+summary(p), nil)
}

// applyApp is `upkeep apply -app <name>`, with the app's name typed in place
// of the terminal's prompt. The engine re-reads afterwards, as it always does.
func (s *Server) applyApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	back := "/apps/" + name
	cfg, err := s.config()
	if err != nil {
		zcadmin.Back(w, r, back, "", err)
		return
	}
	if _, ok := findApp(cfg, name); !ok {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != name {
		s.record("apply", name, "apply refused: the confirmation did not match the app name", false)
		zcadmin.Back(w, r, back, "", errors.New("type the app name to confirm the apply"))
		return
	}
	deploy := r.FormValue("deploy") == "1"
	res := s.apply(r.Context(), cfg, name, deploy)
	detail := "applied"
	if deploy {
		detail += " with deploy"
	}
	if res.Err != nil {
		s.record("apply", name, detail+": "+res.Err.Error(), false)
		zcadmin.Back(w, r, back, "", res.Err)
		return
	}
	if res.Plan.Empty() {
		detail += ": verified, everything matches the config"
	} else {
		detail += ": verified; still " + summary(res.Plan)
	}
	s.record("apply", name, detail, true)
	zcadmin.Back(w, r, back, detail, nil)
}

// surfaceNames lists the surfaces an app declares, in the order the engine
// plans them.
func surfaceNames(app config.App) []string {
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(app.Render != nil, "render")
	add(app.Workers != nil, "workers")
	add(app.Fly != nil, "fly")
	add(app.R2 != nil, "r2")
	add(app.Pages != nil, "pages")
	add(app.Auth != nil, "auth")
	add(app.DNS != nil, "dns")
	add(app.Neon != nil, "neon")
	return out
}

// A nil *Client in an interface is not a nil interface — it would pass a nil
// check and then panic on use. engine.Providers holds interfaces already, so
// a nil check there is honest; these narrow them for status.
func statusRender(p engine.Providers) status.Render {
	if p.Render == nil {
		return nil
	}
	return p.Render
}

func statusCF(p engine.Providers) status.CF {
	if p.CF == nil {
		return nil
	}
	return p.CF
}
