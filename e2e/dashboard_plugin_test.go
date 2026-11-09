package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// decodeOK decodes body, the answer to a request with status, into v,
// failing the test unless status is 200.
func decodeOK(t *testing.T, status int, body string, v any) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status %d:\n%s", status, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
}

func TestMailpitsInboxLinkComesFromItsManifest(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-inbox")
	d.MustRunCLI(serviceProject(t, "dash-inbox-shop", `mailpit = "v1"`+"\n"+`redis = "7"`, "").Dir, "link")

	var list api.ServiceList
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/services", "", nil)
	decodeOK(t, status, body, &list)

	mailpit, ok := serviceIn(list, "mailpit:v1")
	if !ok {
		t.Fatalf("no mailpit:v1 Service: %+v", list)
	}
	want := []api.PluginLink{{Plugin: "mailpit", Label: "Inbox", URL: "https://mail.dash-inbox.localhost/"}}
	if len(mailpit.Links) != 1 || mailpit.Links[0] != want[0] {
		t.Fatalf("mailpit:v1 links %+v, want %+v", mailpit.Links, want)
	}
	if redis, _ := serviceIn(list, "redis:7"); len(redis.Links)+len(redis.Badges)+len(redis.Actions) != 0 {
		t.Fatalf("redis:7 shows Mailpit's Dashboard items: %+v", redis)
	}
}

// toolboxImage is the image of the toolbox test Plugin's Engine, box.
const toolboxImage = "example.test/box"

// toolboxDaemon is a daemon serving its Dashboard at https://<name>.localhost
// (returned) with the toolbox Plugin added, whose fake container engine can
// run the box Engine's image.
func toolboxDaemon(t *testing.T, name string) (*testkit.Daemon, string) {
	t.Helper()
	d, dash := dashboardDaemon(t, name)
	d.Engine.Serve(toolboxImage, containertest.Redis)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "toolbox"))
	return d, dash
}

// projectIn finds the Project called name in list.
func projectIn(list api.ProjectList, name string) (api.Project, bool) {
	for _, p := range list.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return api.Project{}, false
}

func TestAPluginShowsItsLinksBadgesAndActionsOnTheProjectsAndWorktreesThatUseIt(t *testing.T) {
	t.Parallel()
	d, dash := toolboxDaemon(t, "dash-tools")
	shop := gitfixture.New(t, "dash-tools-shop").WriteFile("toolbox.json", "{}").Commit("use the toolbox")
	d.MustRunCLI(shop.Dir, "link")
	plain := gitfixture.New(t, "dash-tools-plain")
	d.MustRunCLI(plain.Dir, "link")
	shop.AddWorktree("feature")
	events := projectEvents(t, d, dash+"/api/v1/events")
	waitForEvent(t, events, "the feature Worktree", func(l api.ProjectList) bool {
		_, ok := worktreeIn(l, "feature.dash-tools-shop.localhost")
		return ok
	})

	var list api.ProjectList
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/projects", "", nil)
	decodeOK(t, status, body, &list)

	p, _ := projectIn(list, "dash-tools-shop")
	if want := []api.PluginLink{{Plugin: "toolbox", Label: "Docs for dash-tools-shop", URL: "https://docs.example.test/dash-tools-shop"}}; !slices.Equal(p.Links, want) {
		t.Errorf("the Project's links %+v, want %+v", p.Links, want)
	}
	if want := []api.PluginBadge{{Plugin: "toolbox", Text: "toolbox"}}; !slices.Equal(p.Badges, want) {
		t.Errorf("the Project's badges %+v, want %+v", p.Badges, want)
	}
	if len(p.Actions) != 1 || p.Actions[0].Label != "Say hello" || p.Actions[0].Command != "hello" ||
		!slices.Equal(p.Actions[0].Args, []string{"dash-tools-shop"}) {
		t.Errorf("the Project's actions %+v, want Say hello running `hello dash-tools-shop`", p.Actions)
	}

	feature, _ := worktreeIn(list, "feature.dash-tools-shop.localhost")
	if want := []api.PluginLink{{Plugin: "toolbox", Label: "Admin", URL: "https://feature.dash-tools-shop.localhost/admin"}}; !slices.Equal(feature.Links, want) {
		t.Errorf("the feature Worktree's links %+v, want %+v", feature.Links, want)
	}
	if want := []api.PluginBadge{{Plugin: "toolbox", Text: "boxed feature", Title: "toolbox is on in feature.dash-tools-shop.localhost"}}; !slices.Equal(feature.Badges, want) {
		t.Errorf("the feature Worktree's badges %+v, want %+v", feature.Badges, want)
	}
	var labels []string
	for _, a := range feature.Actions {
		labels = append(labels, a.Plugin+": "+a.Label)
	}
	if !slices.Equal(labels, []string{"toolbox: Greet", "toolbox: Break"}) {
		t.Errorf("the feature Worktree's actions %q, want Greet and Break", labels)
	}
	if main, _ := worktreeIn(list, "dash-tools-shop.localhost"); len(main.Links) != 1 || main.Links[0].URL != "https://dash-tools-shop.localhost/admin" {
		t.Errorf("the main Worktree's links %+v, want Admin at its Domain", main.Links)
	}

	other, _ := projectIn(list, "dash-tools-plain")
	if len(other.Links)+len(other.Badges)+len(other.Actions) != 0 {
		t.Errorf("a Project that doesn't use the toolbox shows its items: %+v", other.PluginItems)
	}
	if w := other.Worktrees[0]; len(w.Links)+len(w.Badges)+len(w.Actions) != 0 {
		t.Errorf("a Worktree that doesn't use the toolbox shows its items: %+v", w.PluginItems)
	}
}

// runAction runs the Dashboard action a, shown on the Worktree at dir, as
// the Dashboard's page does from origin, and returns the status and body.
func runAction(t *testing.T, d *testkit.Daemon, dash, origin, dir string, a api.PluginAction) (int, string) {
	t.Helper()
	return dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/plugins/"+a.Plugin+"/commands/"+a.Command, origin,
		api.PluginCommandRequest{Dir: dir, Args: a.Args})
}

func TestADashboardActionRunsThePluginsCommandAndShowsItsResult(t *testing.T) {
	t.Parallel()
	d, dash := toolboxDaemon(t, "dash-act")
	shop := gitfixture.New(t, "dash-act-shop").WriteFile("toolbox.json", "{}").Commit("use the toolbox")
	d.MustRunCLI(shop.Dir, "link")
	feature := shop.AddWorktree("feature")
	events := projectEvents(t, d, dash+"/api/v1/events")
	var w api.Worktree
	waitForEvent(t, events, "the feature Worktree's actions", func(l api.ProjectList) bool {
		w, _ = worktreeIn(l, "feature.dash-act-shop.localhost")
		return len(w.Actions) == 2
	})
	greet, broken := w.Actions[0], w.Actions[1]

	for _, origin := range []string{"", "https://dash-act-shop.localhost", "https://evil.example"} {
		if status, body := runAction(t, d, dash, origin, w.Path, greet); status != http.StatusForbidden {
			t.Errorf("an action from Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
	}

	status, body := runAction(t, d, dash, dash, w.Path, greet)
	var res api.PluginCommandResult
	decodeOK(t, status, body, &res)
	if res.Error != "" || len(res.Effects) != 1 || res.Effects[0].Output != "hello feature from "+filepath.Base(feature.Dir)+"\n" {
		t.Fatalf("the Greet action's result: %+v", res)
	}

	status, body = runAction(t, d, dash, dash, w.Path, broken)
	res = api.PluginCommandResult{}
	decodeOK(t, status, body, &res)
	assertContains(t, res.Error, "the toolbox is locked")
}

func TestAServiceShowsTheLinksAndBadgesOfThePluginThatAddsItsEngine(t *testing.T) {
	t.Parallel()
	d, dash := toolboxDaemon(t, "dash-box")
	d.MustRunCLI(serviceProject(t, "dash-box-shop", `box = "1"`+"\n"+`redis = "7"`, "").Dir, "link")

	var list api.ServiceList
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/services", "", nil)
	decodeOK(t, status, body, &list)

	box, _ := serviceIn(list, "box:1")
	if want := []api.PluginLink{{Plugin: "toolbox", Label: "Console", URL: "https://console.dash-box.localhost/box/1"}}; !slices.Equal(box.Links, want) {
		t.Errorf("box:1 links %+v, want %+v", box.Links, want)
	}
	if want := []api.PluginBadge{{Plugin: "toolbox", Text: "box on 7390"}}; !slices.Equal(box.Badges, want) {
		t.Errorf("box:1 badges %+v, want %+v", box.Badges, want)
	}
	if len(box.Actions) != 0 {
		t.Errorf("box:1 actions %+v, want none", box.Actions)
	}
	if redis, _ := serviceIn(list, "redis:7"); len(redis.Links)+len(redis.Badges) != 0 {
		t.Errorf("redis:7 shows the toolbox's items: %+v", redis.PluginItems)
	}

	// Using the Plugin's Engine is using the Plugin.
	var projects api.ProjectList
	status, body = dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/projects", "", nil)
	decodeOK(t, status, body, &projects)
	if p, _ := projectIn(projects, "dash-box-shop"); len(p.Badges) != 1 || p.Badges[0].Text != "toolbox" {
		t.Errorf("a Project using the box Engine shows badges %+v, want the toolbox's", p.Badges)
	}
}

func TestAPluginsDashboardItemsMustBeScopedAndRunItsOwnCommands(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for _, tc := range []struct{ dashboard, want string }{
		{`{"links": [{"scope": "site", "label": "Docs", "url": "https://docs.example.test"}]}`,
			`dashboard.links[0]: scope "site" isn't a scope; use project, worktree or service`},
		{`{"links": [{"scope": "project", "label": "Docs", "url": "javascript:alert(1)"}]}`,
			`dashboard.links[0]: url "javascript:alert(1)" isn't an http or https URL`},
		{`{"links": [{"scope": "project", "label": "", "url": "https://docs.example.test"}]}`,
			`dashboard.links[0]: label isn't set`},
		{`{"badges": [{"scope": "project", "text": "on {port}"}]}`,
			`dashboard.badges[0]: {port} isn't a placeholder a project item has; use {dashboard_domain}, {project}, {domain}`},
		{`{"badges": [{"scope": "worktree", "text": " "}]}`,
			`dashboard.badges[0]: text isn't set`},
		{`{"actions": [{"scope": "worktree", "label": "Dump", "command": "dump"}]}`,
			`dashboard.actions[0]: command "dump" isn't one the Plugin declares under commands`},
		{`{"actions": [{"scope": "service", "label": "Hi", "command": "hello"}]}`,
			`dashboard.actions[0]: an action runs a command, and commands run in a Worktree, so it can't be scoped to a Service`},
	} {
		plugin := withManifest(t, testkit.Plugin(t, "toolbox"), `{
  "name": "toolbox",
  "version": "0.1.0",
  "api_version": 1,
  "integrations": [{"name": "toolbox", "detect": {"file": "toolbox.json"}}],
  "commands": [{"name": "hello", "summary": "Say hello"}],
  "dashboard": `+tc.dashboard+`
}`)
		res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)
		if res.ExitCode == 0 {
			t.Errorf("adding a Plugin with dashboard %s exited 0:\n%s", tc.dashboard, res.Output())
			continue
		}
		assertContains(t, res.Stderr, tc.want)
	}

	// A Service item shows on Services of the Plugin's Engines: it needs one.
	plugin := withManifest(t, testkit.Plugin(t, "toolbox"), `{
  "name": "toolbox",
  "version": "0.1.0",
  "api_version": 1,
  "integrations": [{"name": "toolbox", "detect": {"file": "toolbox.json"}}],
  "dashboard": {"badges": [{"scope": "service", "text": "boxed"}]}
}`)
	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)
	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin with a Service badge but no Engine exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "dashboard.badges[0]: it is scoped to a Service, so it shows on Services of the Plugin's Engines, but the Plugin adds no Engine")
	if out := d.MustRunCLI(d.Home, "plugin", "list").Stdout; !strings.Contains(out, "No Plugins are installed") {
		t.Fatalf("a refused Plugin was installed:\n%s", out)
	}
}
