package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/naming"
	"github.com/lanewayhq/laneway/internal/registry"
)

// aliasDomain is an Alias's Domain: <alias>.<project>.localhost.
func aliasDomain(p registry.Project, a registry.Alias) string {
	return a.Name + "." + domainOf(p)
}

// aliasDomains are the Domains of the Aliases of the Project's Worktree
// called worktree ("" for the main Worktree), sorted.
func (d *Daemon) aliasDomains(p registry.Project, worktree string) []string {
	var out []string
	for _, a := range d.registry.Aliases(p.Path) {
		if a.Worktree == worktree {
			out = append(out, aliasDomain(p, a))
		}
	}
	return out
}

// primaryDomain is the Primary Domain of the Project's Worktree w: its
// newest Alias's Domain, or its own Domain if it has none. It is "" while
// w's Domain is (a Worktree not discovered yet).
func (d *Daemon) primaryDomain(p registry.Project, w worktreeRef) string {
	if w.domain == "" {
		return ""
	}
	if a, ok := d.registry.NewestAlias(p.Path, w.name); ok {
		return aliasDomain(p, a)
	}
	return w.domain
}

// servedPrimaryDomain is the Primary Domain of the served Worktree s; false
// once its Project is no longer linked.
func (d *Daemon) servedPrimaryDomain(s *served) (string, bool) {
	p, ok := d.servedProject(s)
	if !ok {
		return "", false
	}
	return d.primaryDomain(p, s.worktreeRef), true
}

// servedProject is the Project of the served Worktree s; false once it is
// no longer linked.
func (d *Daemon) servedProject(s *served) (registry.Project, bool) {
	for _, p := range d.registry.Projects() {
		if p.Name == s.project {
			return p, true
		}
	}
	return registry.Project{}, false
}

// changeAliases makes change, a change to the Aliases of the Project's
// Worktree w, and reports whether it moved w's Primary Domain. If it did,
// w catches up with it (primaryDomainMoved). Either way, the origins w's
// Buckets allow follow its Aliases (worktreeOriginsChanged). change runs
// under syncMu, so a Worktree discovered meanwhile can't be given the same
// label.
func (d *Daemon) changeAliases(p registry.Project, w worktreeRef, change func() error) (moved bool, err error) {
	d.syncMu.Lock()
	before := d.primaryDomain(p, w)
	err = change()
	after := d.primaryDomain(p, w)
	d.syncMu.Unlock()
	if err != nil {
		return false, err
	}
	// Every Alias is an origin its Buckets allow, newest or not.
	d.worktreeOriginsChanged(p, w)
	if before != after {
		d.primaryDomainMoved(w.domain)
		d.primaryDomainChanged(p, w, before)
	}
	return before != after, nil
}

// aliasTarget is the Domain of the Worktree that host is an Alias of.
func (d *Daemon) aliasTarget(host string) (string, bool) {
	name, rest, ok := strings.Cut(host, ".")
	if !ok {
		return "", false
	}
	for _, p := range d.registry.Projects() {
		if domainOf(p) != rest {
			continue
		}
		for _, a := range d.registry.Aliases(p.Path) {
			if a.Name == name {
				domain := d.namedWorktreeDomain(p, a.Worktree)
				return domain, domain != ""
			}
		}
	}
	return "", false
}

// reservedLabels are the names a linked Worktree's label or an Alias must
// not take: the main Worktree's HTTP Processes other than the Primary
// Process, which are served at <process>.<project>.localhost.
func (d *Daemon) reservedLabels(p registry.Project) []string {
	cfg, err := d.plugins.ResolveDir(p.Path)
	if err != nil {
		return nil
	}
	var names []string
	for _, proc := range cfg.Processes {
		if proc.HTTP && !proc.Primary {
			names = append(names, proc.Name)
		}
	}
	return names
}

// describeWorktree names a Worktree in messages.
func describeWorktree(name string) string {
	if name == "" {
		return "the main Worktree"
	}
	return fmt.Sprintf("Worktree %q", name)
}

// addAlias adds the Alias name to the Worktree containing dir.
func (d *Daemon) addAlias(ctx context.Context, dir, name string) (api.AliasResponse, error) {
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.AliasResponse{}, err
	}
	if name == "" {
		return api.AliasResponse{}, &api.Error{Code: api.CodeInvalidAlias, Message: "an Alias needs a name, such as api-v2"}
	}
	if label := naming.DomainLabel(name); label != name {
		return api.AliasResponse{}, &api.Error{
			Code: api.CodeInvalidAlias,
			Message: fmt.Sprintf("%q can't be an Alias: use lowercase letters, digits and '-' "+
				"(at most 63 characters), such as %q", name, label),
		}
	}
	w, ok := d.worktreeRefOf(p, checkout.Dir)
	if !ok {
		// A Worktree the Watcher hasn't reported yet.
		d.syncWorktrees(ctx, p)
		if w, ok = d.worktreeRefOf(p, checkout.Dir); !ok {
			return api.AliasResponse{}, &api.Error{
				Code:    api.CodeNotLinked,
				Message: fmt.Sprintf("Worktree %q hasn't been discovered yet; try again in a moment", checkout.Name),
			}
		}
	}
	alias := registry.Alias{Project: p.Path, Worktree: checkout.Name, Name: name}
	var existed bool
	moved, err := d.changeAliases(p, w, func() error {
		existed = slices.Contains(d.registry.Aliases(p.Path), alias)
		if slices.Contains(d.reservedLabels(p), name) {
			return &api.Error{
				Code: api.CodeAliasTaken,
				Message: fmt.Sprintf("%s can't be an Alias: it is where the main Worktree's %q Process is served; "+
					"choose another name", aliasDomain(p, alias), name),
			}
		}
		return d.registry.AddAlias(alias)
	})
	if taken, ok := errors.AsType[*registry.AliasTakenError](err); ok {
		what := "the Domain"
		if taken.IsAlias {
			what = "an Alias"
		}
		return api.AliasResponse{}, &api.Error{
			Code: api.CodeAliasTaken,
			Message: fmt.Sprintf("%s is already %s of %s in Project %q; choose another name",
				aliasDomain(p, alias), what, describeWorktree(taken.Worktree), p.Name),
		}
	}
	if err != nil {
		return api.AliasResponse{}, err
	}
	info, err := d.worktreeInfo(ctx, dir)
	return api.AliasResponse{Domain: aliasDomain(p, alias), Worktree: info, Moved: moved, Existed: existed}, err
}

// removeAlias removes the Alias name from the Worktree containing dir.
func (d *Daemon) removeAlias(ctx context.Context, dir, name string) (api.AliasResponse, error) {
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.AliasResponse{}, err
	}
	alias := registry.Alias{Project: p.Path, Worktree: checkout.Name, Name: name}
	w, _ := d.worktreeRefOf(p, checkout.Dir)
	moved, err := d.changeAliases(p, w, func() error { return d.registry.RemoveAlias(alias) })
	if errors.Is(err, registry.ErrNoAlias) {
		has := "it has none"
		if domains := d.aliasDomains(p, checkout.Name); len(domains) > 0 {
			has = "it has " + strings.Join(domains, ", ")
		}
		return api.AliasResponse{}, &api.Error{
			Code:    api.CodeUnknownAlias,
			Message: fmt.Sprintf("%s has no Alias %s; %s", describeWorktree(checkout.Name), aliasDomain(p, alias), has),
		}
	}
	if err != nil {
		return api.AliasResponse{}, err
	}
	info, err := d.worktreeInfo(ctx, dir)
	return api.AliasResponse{Domain: aliasDomain(p, alias), Worktree: info, Moved: moved}, err
}
