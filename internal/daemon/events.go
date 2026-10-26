package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
)

// eventsInterval is how often GET /v1/events looks for changes. It is
// polling, so it uses real time, not the clock.
const eventsInterval = 500 * time.Millisecond

// projectList is every Project with its Worktrees' states: the body of
// GET /v1/projects and of each `projects` event.
func (d *Daemon) projectList() api.ProjectList {
	list := api.ProjectList{Projects: []api.Project{}}
	for _, p := range d.registry.Projects() {
		ap := d.apiProject(p)
		for i, w := range ap.Worktrees {
			w = d.withState(w)
			w.Declared = declaredProcesses(w.Path)
			ap.Worktrees[i] = w
		}
		list.Projects = append(list.Projects, ap)
	}
	return list
}

// declaredProcesses are the Processes the config of the Worktree at dir
// declares; nil if it can't be read.
func declaredProcesses(dir string) []api.DeclaredProcess {
	cfg, err := recipe.ResolveDir(dir)
	if err != nil {
		return nil
	}
	var procs []api.DeclaredProcess
	for _, p := range cfg.Processes {
		procs = append(procs, api.DeclaredProcess{
			Name: p.Name, StartMode: p.StartMode.String(), Primary: p.Primary, HTTP: p.HTTP,
		})
	}
	return procs
}

// handleEvents is GET /v1/events, a stream of server-sent events: a
// `projects` event (data = the api.ProjectList GET /v1/projects returns),
// a `services` event (data = the api.ServiceList of GET /v1/services) and
// an `orphans` event (data = the api.OrphanList of GET /v1/orphans) at
// once, then another of each whenever it changes. It ends when the client
// goes away or the daemon shuts down.
//
// Changes are found by comparing each list every eventsInterval, so
// anything those routes report shows up here without its code having to
// announce it.
func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	feeds := []struct {
		event string
		read  func() any
	}{
		{"projects", func() any { return d.projectList() }},
		{"services", func() any { return d.listServices() }},
		{"orphans", func() any { return d.listOrphans() }},
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	tick := time.NewTicker(eventsInterval)
	defer tick.Stop()
	last := make([][]byte, len(feeds))
	for {
		sent := false
		for i, f := range feeds {
			b, err := json.Marshal(f.read())
			if err != nil {
				d.log.Error("encoding event", "event", f.event, "err", err)
				return
			}
			if bytes.Equal(b, last[i]) {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f.event, b); err != nil {
				return
			}
			last[i] = b
			sent = true
		}
		if sent {
			if err := rc.Flush(); err != nil {
				return
			}
		}
		select {
		case <-tick.C:
		case <-r.Context().Done():
			return
		case <-d.closing:
			return
		}
	}
}
