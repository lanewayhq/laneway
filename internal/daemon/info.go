package daemon

import (
	"cmp"
	"context"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/service"
	"github.com/lanewayhq/laneway/internal/supervisor"
)

// info is everything about the Worktree containing dir, which must be inside
// a Linked Project: the body of GET /v1/info and `lane info --json`.
func (d *Daemon) info(ctx context.Context, dir string) (api.Info, error) {
	wt, err := d.worktreeInfo(ctx, dir)
	if err != nil {
		return api.Info{}, err
	}
	info := api.Info{
		SchemaVersion:    api.InfoSchemaVersion,
		Project:          api.InfoProject{Name: wt.Project.Name, Path: wt.Project.Path},
		Worktree:         api.InfoWorktree{Name: wt.Worktree.Name, Path: wt.Worktree.Path, Main: wt.Worktree.Main},
		Domain:           wt.Domain,
		URL:              wt.URL,
		Aliases:          []api.InfoAlias{},
		State:            d.worktreeState(wt.Domain),
		Processes:        []api.InfoProcess{},
		Services:         []api.InfoService{},
		Integrations:     []string{},
		IntegrationRules: []api.InfoIntegrationRule{},
	}
	for _, domain := range wt.Worktree.Aliases {
		info.Aliases = append(info.Aliases, api.InfoAlias{Domain: domain, URL: domainURL(domain)})
	}
	if s := wt.Worktree.Setup; s != nil {
		info.Setup = &api.InfoSetup{State: s.State, Step: s.Step, Error: s.Error,
			Output: orEmpty(s.Output), Warnings: orEmpty(s.Warnings)}
	}
	dir = wt.Worktree.Path
	cfg, cfgErr := d.plugins.ResolveDir(dir)
	if cfgErr == nil {
		info.Processes = processInfo(cfg.Processes, d.supervisor.Processes(dir), wt.Worktree.PrimaryDomain)
	}
	if wt.Database != nil {
		info.Database = &api.InfoDatabase{Engine: wt.Database.Engine, Name: wt.Database.Name, Test: wt.Database.Test}
		if cfgErr == nil {
			if s, ok := cfg.DatabaseService(); ok {
				info.Database.Service = s.Name()
			}
		}
	}
	if cfgErr == nil {
		info.Services = d.infoServices(cfg.Services)
		info.Recipe, info.RecipeRule = cfg.Recipe, cfg.RecipeRule
		info.Integrations = orEmpty(cfg.Integrations)
		for _, name := range cfg.Integrations {
			info.IntegrationRules = append(info.IntegrationRules, api.InfoIntegrationRule{Name: name, Rule: cfg.IntegrationRules[name]})
		}
	} else {
		info.ConfigError = cfgErr.Error()
	}
	return info, nil
}

// processInfo merges the declared Processes with the ones the Supervisor
// runs: every declared one (stopped unless it runs), then any that run but
// are no longer declared. HTTP Processes are given out under the
// Worktree's Primary Domain.
func processInfo(declared []recipe.Process, runs []supervisor.Status, primaryDomain string) []api.InfoProcess {
	state := map[string]string{}
	for _, st := range runs {
		state[st.Name] = string(st.State)
	}
	out := []api.InfoProcess{}
	for _, p := range declared {
		ip := api.InfoProcess{Name: p.Name, StartMode: p.StartMode.String(), Primary: p.Primary, HTTP: p.HTTP,
			State: cmp.Or(state[p.Name], string(supervisor.StateStopped))}
		delete(state, p.Name)
		switch {
		case primaryDomain == "":
		case p.Primary:
			ip.URL = domainURL(primaryDomain)
		case p.HTTP:
			ip.URL = domainURL(p.Name + "." + primaryDomain)
		}
		out = append(out, ip)
	}
	for _, st := range runs {
		if _, undeclared := state[st.Name]; undeclared {
			out = append(out, api.InfoProcess{Name: st.Name, State: string(st.State)})
		}
	}
	return out
}

// infoServices are services as `lane info` shows them. A Service not
// served yet is stopped, at the port it would be given.
func (d *Daemon) infoServices(services []recipe.Service) []api.InfoService {
	out := []api.InfoService{}
	for _, s := range services {
		e, ok := engine.Lookup(s.Engine)
		if !ok {
			continue
		}
		st, ok := d.services.Status(s.Name())
		if !ok {
			rs, err := d.registry.PlanService(s, e.Ports(s.Version))
			if err != nil {
				continue
			}
			st = service.Status{Service: service.Service{Engine: e, Version: s.Version, Port: rs.Port},
				Host: d.serviceHost(), State: service.Stopped}
		}
		as := apiService(st)
		out = append(out, api.InfoService{Name: as.Name, Engine: as.Engine, Version: as.Version,
			Host: as.Host, Port: as.Port, User: as.User, Password: as.Password, State: as.State, Error: as.Error})
	}
	return out
}

// orEmpty is list, or an empty list if it is nil, so it is [] in JSON.
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
