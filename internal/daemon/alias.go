package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/naming"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
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
func reservedLabels(p registry.Project) []string {
	cfg, err := recipe.ResolveDir(p.Path)
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
	if d.checkoutDomain(p, checkout) == "" {
		// A Worktree the Watcher hasn't reported yet.
		d.syncWorktrees(ctx, p)
		if d.checkoutDomain(p, checkout) == "" {
			return api.AliasResponse{}, &api.Error{
				Code:    api.CodeNotLinked,
				Message: fmt.Sprintf("Worktree %q hasn't been discovered yet; try again in a moment", checkout.Name),
			}
		}
	}
	alias := registry.Alias{Project: p.Path, Worktree: checkout.Name, Name: name}
	// Serialised with syncWorktrees, so a Worktree discovered meanwhile
	// can't be given the same label.
	d.syncMu.Lock()
	if slices.Contains(reservedLabels(p), name) {
		err = &api.Error{
			Code: api.CodeAliasTaken,
			Message: fmt.Sprintf("%s can't be an Alias: it is where the main Worktree's %q Process is served; "+
				"choose another name", aliasDomain(p, alias), name),
		}
	} else {
		err = d.registry.AddAlias(alias)
	}
	d.syncMu.Unlock()
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
	return api.AliasResponse{Domain: aliasDomain(p, alias), Worktree: info}, err
}

// removeAlias removes the Alias name from the Worktree containing dir.
func (d *Daemon) removeAlias(ctx context.Context, dir, name string) (api.AliasResponse, error) {
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.AliasResponse{}, err
	}
	alias := registry.Alias{Project: p.Path, Worktree: checkout.Name, Name: name}
	err = d.registry.RemoveAlias(alias)
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
	return api.AliasResponse{Domain: aliasDomain(p, alias), Worktree: info}, err
}
