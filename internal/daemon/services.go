package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/service"
)

// Services (package service) are shared by every Project using them. The
// daemon serves every Service recorded in the Registry from the start, and
// records a Service (giving it its port for good) the first time a
// Worktree's config declares it: when the Worktree is served and every time
// it starts, since config is re-read then.

// serveRecordedServices serves the Services the Registry has ports for.
func (d *Daemon) serveRecordedServices() {
	for _, rs := range d.registry.Services() {
		e, ok := service.Lookup(rs.Engine)
		if !ok {
			d.log.Warn("unknown Service in the Registry", "engine", rs.Engine)
			continue
		}
		d.services.Serve(service.Service{Engine: e, Version: rs.Version, Port: rs.Port})
	}
}

// serveServices records and serves the Services in services.
func (d *Daemon) serveServices(services []recipe.Service) {
	for _, s := range services {
		e, ok := service.Lookup(s.Engine)
		if !ok {
			continue // config rejects unknown engines
		}
		rs, err := d.registry.AddService(s, e.Ports(s.Version))
		if err != nil {
			d.log.Error("recording Service", "service", s.Name(), "err", err)
			continue
		}
		d.services.Serve(service.Service{Engine: e, Version: rs.Version, Port: rs.Port})
	}
}

// serveServicesOf serves the Services the config of the Worktree at dir
// declares. A config error is reported when the Worktree starts.
func (d *Daemon) serveServicesOf(dir string) {
	if cfg, err := d.plugins.ResolveDir(dir); err == nil {
		d.serveServices(cfg.Services)
	}
}

// serviceVars are the Env Patch variables of services ({redis_port}, …).
// It records nothing: a Service that has no port yet gets the one it would
// be given.
func (d *Daemon) serviceVars(services []recipe.Service) map[string]string {
	vars := map[string]string{}
	for _, s := range services {
		e, ok := service.Lookup(s.Engine)
		if !ok {
			continue
		}
		rs, err := d.registry.PlanService(s, e.Ports(s.Version))
		if err != nil {
			continue
		}
		for k, v := range e.Values(d.serviceHost(), rs.Port) {
			vars[k] = v
		}
	}
	return vars
}

// serviceHost is where clients reach Services.
func (d *Daemon) serviceHost() string {
	return d.cfg.ServiceHosts[0]
}

// useServices records that a Worktree using services is running, so they
// don't go Idle while it does. Call the returned func, once, when it stops.
func (d *Daemon) useServices(services []recipe.Service) func() {
	var releases []func()
	for _, s := range services {
		releases = append(releases, d.services.Use(s.Name()))
	}
	return sync.OnceFunc(func() {
		for _, release := range releases {
			release()
		}
	})
}

// holdAlwaysServices makes the Worktree hold its Services up while its
// always Processes run, whether or not its lazy ones do (it may only have
// always Processes, or have gone Idle): they use them too. Call it, holding
// s.lifecycle, before starting them; down and stopWorktree release the hold.
func (d *Daemon) holdAlwaysServices(s *served, cfg recipe.Resolved) {
	if !slices.ContainsFunc(cfg.Processes, func(p recipe.Process) bool { return p.StartMode == recipe.Always }) {
		releaseAlwaysServices(s)
		return
	}
	if s.alwaysServices == nil {
		s.alwaysServices = d.useServices(cfg.Services)
	}
}

// releaseAlwaysServices ends the hold of holdAlwaysServices, if any. The
// caller holds s.lifecycle.
func releaseAlwaysServices(s *served) {
	if s.alwaysServices != nil {
		s.alwaysServices()
		s.alwaysServices = nil
	}
}

// releaseServices ends the use of Services by the Worktree st started,
// when it stops or st is dropped. It does nothing if st didn't succeed.
func (st *startAttempt) releaseServices() {
	if st.release != nil {
		st.release()
	}
}

// serviceIdleTimeout is service_idle_timeout from the global config, read
// whenever a Service starts; 0 (the default) if unset or unreadable.
func (d *Daemon) serviceIdleTimeout() time.Duration {
	global, err := config.LoadGlobal(d.cfg.Paths.GlobalConfig())
	if err != nil {
		d.log.Error("reading the global config", "err", err)
		return 0
	}
	return ptrValue(global.ServiceIdleTimeout)
}

// apiService is the API view of a Service.
func apiService(st service.Status) api.Service {
	return api.Service{
		Name:     st.Name(),
		Engine:   st.Engine.Name,
		Version:  st.Version,
		Host:     st.Host,
		Port:     st.Port,
		User:     st.Engine.User,
		Password: service.Password,
		State:    st.State,
		Error:    st.Error,
	}
}

func (d *Daemon) listServices() api.ServiceList {
	list := api.ServiceList{Services: []api.Service{}}
	for _, st := range d.services.List() {
		list.Services = append(list.Services, apiService(st))
	}
	return list
}

// pickServices resolves a ServiceRequest's name: "redis:7", "redis" when
// only one redis Service (or the default version) is served, or "" for
// every Service.
func (d *Daemon) pickServices(name string) ([]string, error) {
	all := d.services.List()
	var names []string
	for _, st := range all {
		names = append(names, st.Name())
	}
	if name == "" {
		return names, nil
	}
	var matches []service.Status
	for _, st := range all {
		switch {
		case st.Name() == name:
			return []string{name}, nil
		case st.Engine.Name == name:
			matches = append(matches, st)
		}
	}
	if len(matches) == 1 {
		return []string{matches[0].Name()}, nil
	}
	for _, st := range matches {
		if st.Version == st.Engine.DefaultVersion {
			return []string{st.Name()}, nil
		}
	}
	known := "none yet: a Project declares one in the [services] table of " + config.FileName
	if len(names) > 0 {
		known = strings.Join(names, ", ")
	}
	return nil, &api.Error{Code: api.CodeUnknownService,
		Message: fmt.Sprintf("there is no Service %q; the Services are %s", name, known)}
}

// controlServices starts (up) or stops (down) the Services req names.
func (d *Daemon) controlServices(ctx context.Context, action string, req api.ServiceRequest) (api.ServiceList, error) {
	if action != "up" && action != "down" {
		return api.ServiceList{}, &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf("unknown action %q", action)}
	}
	names, err := d.pickServices(req.Name)
	if err != nil {
		return api.ServiceList{}, err
	}
	list := api.ServiceList{Services: []api.Service{}}
	var failed []string
	for _, name := range names {
		if action == "up" {
			err = d.services.Up(ctx, name)
		} else {
			err = d.services.Down(ctx, name)
		}
		if err != nil {
			failed = append(failed, err.Error())
		}
		st, _ := d.services.Status(name)
		list.Services = append(list.Services, apiService(st))
	}
	if len(failed) > 0 {
		return list, &api.Error{Code: api.CodeServiceFailed, Message: strings.Join(failed, "; ")}
	}
	return list, nil
}
