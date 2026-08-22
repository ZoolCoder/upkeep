package web

// Settings: the password, and what the command line decided — config path,
// data files, which credentials were found and where. Never a value.

import (
	"net/http"

	"github.com/zoolcoder/zcadmin"
)

type settingsPage struct {
	chrome
	ConfigPath   string
	AuthFile     string
	ActivityFile string
	Credentials  []Credential
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "settings.html", settingsPage{
		chrome: s.chrome(r, "settings"), ConfigPath: s.opts.ConfigPath,
		AuthFile: s.opts.AuthFile, ActivityFile: s.opts.ActivityFile,
		Credentials: s.credentials(),
	})
}

func (s *Server) settingsPassword(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.ChangePassword(r.FormValue("current"), r.FormValue("password"), r.FormValue("confirm")); err != nil {
		zcadmin.Back(w, r, "/settings", "", err)
		return
	}
	zcadmin.Back(w, r, "/settings", "password changed — other browsers stay signed in until the server restarts", nil)
}
