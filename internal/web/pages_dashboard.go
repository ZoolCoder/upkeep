package web

// Dashboard: "what needs attention?" — stat cards from the last plan this
// process ran, one card per app, recent activity.

import (
	"net/http"
	"strconv"

	"github.com/zoolcoder/zcadmin"
)

type appCard struct {
	Name     string
	Surfaces []string
	// Planned is when the last run happened, or "" for never.
	Planned string
	Summary string
	Counts  counts
	Err     string
}

type activityRow struct {
	zcadmin.Activity
	When   string
	Search string
}

type dashboardPage struct {
	chrome
	ConfigPath   string
	ConfigErr    string
	Apps         []appCard
	Planned      int // apps with a run in this process
	Outstanding  counts
	CredsOK      int
	CredsTotal   int
	CredsMissing int
	RecentAct    []activityRow
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	p := dashboardPage{chrome: s.chrome(r, "dashboard"), ConfigPath: s.opts.ConfigPath}
	cfg, err := s.config()
	if err != nil {
		p.ConfigErr = err.Error()
	}
	for _, app := range cfg.Apps {
		p.Apps = append(p.Apps, s.card(app.Name, surfaceNames(app)))
	}
	for _, c := range p.Apps {
		if c.Planned == "" {
			continue
		}
		p.Planned++
		p.Outstanding.Create += c.Counts.Create
		p.Outstanding.Update += c.Counts.Update
		p.Outstanding.Delete += c.Counts.Delete
		p.Outstanding.Manual += c.Counts.Manual
	}
	for _, c := range s.credentials() {
		p.CredsTotal++
		if c.Present {
			p.CredsOK++
		} else {
			p.CredsMissing++
		}
	}
	acts, _ := s.log.Recent(8)
	for _, a := range acts {
		p.RecentAct = append(p.RecentAct, activityRow{Activity: a, When: s.ago(a.At)})
	}
	s.render(w, "dashboard.html", p)
}

func (s *Server) card(name string, surfaces []string) appCard {
	c := appCard{Name: name, Surfaces: surfaces}
	if last, ok := s.lastRun(name); ok {
		c.Planned = s.ago(last.At)
		c.Summary = summary(last.Plan)
		c.Counts = count(last.Plan)
		if last.Err != nil {
			c.Err = last.Err.Error()
		}
	}
	return c
}

// planAll runs a plan for every app, as `upkeep plan` does, one app at a time
// so each card remembers its own.
func (s *Server) planAll(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.config()
	if err != nil {
		zcadmin.Back(w, r, "/", "", err)
		return
	}
	var changes, manual, failed int
	for _, app := range cfg.Apps {
		p, err := s.plan(r.Context(), cfg, app.Name)
		if err != nil {
			failed++
			s.record("plan", app.Name, "plan failed: "+err.Error(), false)
			continue
		}
		changes += len(p.Executable())
		manual += len(p.Manual())
		s.record("plan", app.Name, "planned: "+summary(p), true)
	}
	msg := "planned " + strconv.Itoa(len(cfg.Apps)) + " app(s): " +
		strconv.Itoa(changes) + " change(s) upkeep can make, " + strconv.Itoa(manual) + " manual"
	if failed > 0 {
		msg += ", " + strconv.Itoa(failed) + " failed to read"
	}
	zcadmin.Back(w, r, "/", msg, nil)
}
