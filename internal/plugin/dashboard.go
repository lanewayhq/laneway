package plugin

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Dashboard links, badges and actions from Plugins (spec #48): a Plugin
// declares them as data in its manifest, each scoped to a Project, a
// Worktree or a Service, and the Dashboard draws them. Plugins supply no UI
// of their own. An action runs one of the Plugin's commands.

// Scopes a Dashboard item can have: what it is shown on.
const (
	// ScopeProject items show on each Project that uses the Plugin.
	ScopeProject = "project"
	// ScopeWorktree items show on each Worktree that uses the Plugin.
	ScopeWorktree = "worktree"
	// ScopeService items show on each Service of an Engine the Plugin adds.
	ScopeService = "service"
)

// scopeVars are the placeholders, such as {domain}, an item's label, text,
// URL and args may use in each scope.
var scopeVars = map[string][]string{
	ScopeProject:  {"dashboard_domain", "project", "domain"},
	ScopeWorktree: {"dashboard_domain", "project", "worktree", "domain"},
	ScopeService:  {"dashboard_domain", "service", "engine", "version", "host", "port"},
}

// Dashboard is what a Plugin shows on the Dashboard.
type Dashboard struct {
	Links   []LinkDecl   `json:"links,omitempty"`
	Badges  []BadgeDecl  `json:"badges,omitempty"`
	Actions []ActionDecl `json:"actions,omitempty"`
}

// LinkDecl is a link, such as Mailpit's Inbox.
type LinkDecl struct {
	Scope string `json:"scope"`
	Label string `json:"label"`
	// URL is an http or https URL.
	URL string `json:"url"`
}

// BadgeDecl is a short label, with an optional Title that says more.
type BadgeDecl struct {
	Scope string `json:"scope"`
	Text  string `json:"text"`
	Title string `json:"title,omitempty"`
}

// ActionDecl is a button that runs the Plugin's Command with Args in the
// Worktree it is shown on (a Project's main Worktree). Commands run in a
// Worktree, so an action can't be scoped to a Service.
type ActionDecl struct {
	Scope   string   `json:"scope"`
	Label   string   `json:"label"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// placeholder is a placeholder in an item, such as {domain}.
var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// validateDashboard checks the Dashboard items m declares.
func (m Manifest) validateDashboard() error {
	d := m.Dashboard
	for i, l := range d.Links {
		at := fmt.Sprintf("dashboard.links[%d]", i)
		if err := m.checkScope(at, l.Scope); err != nil {
			return err
		}
		if strings.TrimSpace(l.Label) == "" {
			return fmt.Errorf("%s: label isn't set; set it to the link's text, such as \"Inbox\"", at)
		}
		if !strings.HasPrefix(l.URL, "https://") && !strings.HasPrefix(l.URL, "http://") {
			return fmt.Errorf("%s: url %q isn't an http or https URL, such as \"https://{domain}/admin\"", at, l.URL)
		}
		if err := checkPlaceholders(at, l.Scope, l.Label, l.URL); err != nil {
			return err
		}
	}
	for i, b := range d.Badges {
		at := fmt.Sprintf("dashboard.badges[%d]", i)
		if err := m.checkScope(at, b.Scope); err != nil {
			return err
		}
		if strings.TrimSpace(b.Text) == "" {
			return fmt.Errorf("%s: text isn't set; set it to what the badge says", at)
		}
		if err := checkPlaceholders(at, b.Scope, b.Text, b.Title); err != nil {
			return err
		}
	}
	for i, a := range d.Actions {
		at := fmt.Sprintf("dashboard.actions[%d]", i)
		if a.Scope == ScopeService {
			return fmt.Errorf("%s: an action runs a command, and commands run in a Worktree, so it can't be scoped to a Service; scope it to %s or %s",
				at, ScopeWorktree, ScopeProject)
		}
		if err := m.checkScope(at, a.Scope); err != nil {
			return err
		}
		if strings.TrimSpace(a.Label) == "" {
			return fmt.Errorf("%s: label isn't set; set it to the button's text", at)
		}
		if !m.HasCommand(a.Command) {
			return fmt.Errorf("%s: command %q isn't one the Plugin declares under commands; an action runs one of the Plugin's commands", at, a.Command)
		}
		if err := checkPlaceholders(at, a.Scope, append([]string{a.Label}, a.Args...)...); err != nil {
			return err
		}
	}
	return nil
}

// checkScope checks the scope of the item at.
func (m Manifest) checkScope(at, scope string) error {
	if _, ok := scopeVars[scope]; !ok {
		return fmt.Errorf("%s: scope %q isn't a scope; use %s, %s or %s", at, scope, ScopeProject, ScopeWorktree, ScopeService)
	}
	if scope == ScopeService && len(m.EngineFiles) == 0 {
		return fmt.Errorf("%s: it is scoped to a Service, so it shows on Services of the Plugin's Engines, but the Plugin adds no Engine; scope it to %s or %s",
			at, ScopeProject, ScopeWorktree)
	}
	return nil
}

// checkPlaceholders checks the placeholders in texts are ones scope has.
func checkPlaceholders(at, scope string, texts ...string) error {
	for _, text := range texts {
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			if !slices.Contains(scopeVars[scope], m[1]) {
				return fmt.Errorf("%s: {%s} isn't a placeholder a %s item has; use %s",
					at, m[1], scope, "{"+strings.Join(scopeVars[scope], "}, {")+"}")
			}
		}
	}
	return nil
}

// In is what d shows in scope, with placeholders replaced by vars (see
// scopeVars).
func (d Dashboard) In(scope string, vars map[string]string) Dashboard {
	fill := func(s string) string {
		return placeholder.ReplaceAllStringFunc(s, func(p string) string {
			return vars[p[1:len(p)-1]]
		})
	}
	var out Dashboard
	for _, l := range d.Links {
		if l.Scope == scope {
			out.Links = append(out.Links, LinkDecl{Scope: scope, Label: fill(l.Label), URL: fill(l.URL)})
		}
	}
	for _, b := range d.Badges {
		if b.Scope == scope {
			out.Badges = append(out.Badges, BadgeDecl{Scope: scope, Text: fill(b.Text), Title: fill(b.Title)})
		}
	}
	for _, a := range d.Actions {
		if a.Scope == scope {
			var args []string
			for _, arg := range a.Args {
				args = append(args, fill(arg))
			}
			out.Actions = append(out.Actions, ActionDecl{Scope: scope, Label: fill(a.Label), Command: a.Command, Args: args})
		}
	}
	return out
}

// Showing are the Plugins that declare Dashboard items: the built-in ones
// first, then the installed ones, by name.
func (h *Host) Showing() []Plugin {
	var out []Plugin
	for _, p := range append(builtin(), h.List()...) {
		d := p.Dashboard
		if len(d.Links)+len(d.Badges)+len(d.Actions) > 0 {
			out = append(out, p)
		}
	}
	return out
}
