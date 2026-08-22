package web

// Activity: the JSONL log, newest first, filtered by kind.

import (
	"net/http"
	"sort"
	"strings"
)

type activityPage struct {
	chrome
	Kind  string
	Kinds []string
	Rows  []activityRow
	Limit int
	Path  string
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	p := activityPage{chrome: s.chrome(r, "activity"), Kind: r.URL.Query().Get("kind"), Limit: activityLimit, Path: s.opts.ActivityFile}
	all, err := s.log.Recent(0)
	if err != nil {
		s.fail(w, err)
		return
	}
	kinds := map[string]bool{}
	for _, a := range all {
		kinds[a.Kind] = true
		if p.Kind != "" && a.Kind != p.Kind {
			continue
		}
		if len(p.Rows) >= activityLimit {
			continue
		}
		p.Rows = append(p.Rows, activityRow{
			Activity: a, When: s.ago(a.At),
			Search: strings.ToLower(a.Kind + " " + a.Target + " " + a.Detail),
		})
	}
	for k := range kinds {
		p.Kinds = append(p.Kinds, k)
	}
	sort.Strings(p.Kinds)
	s.render(w, "activity.html", p)
}
