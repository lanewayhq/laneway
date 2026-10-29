package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// infoJSON runs `portby info --json` in dir and decodes its output without
// Portby's own types, so the test sees the keys an agent sees.
func infoJSON(t *testing.T, d *testkit.Daemon, dir string) map[string]any {
	t.Helper()
	res := d.MustRunCLI(dir, "info", "--json")
	var info map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &info); err != nil {
		t.Fatalf("`portby info --json` printed something that isn't a JSON object: %v\n%s", err, res.Stdout)
	}
	return info
}

// at is the value at path (object keys and list indexes) in v.
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for i, key := range path {
		switch k := key.(type) {
		case string:
			obj, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v is %T, not an object", path[:i], v)
			}
			if v, ok = obj[k]; !ok {
				t.Fatalf("%v has no key %q: %v", path[:i], k, obj)
			}
		case int:
			list, ok := v.([]any)
			if !ok || k >= len(list) {
				t.Fatalf("%v is %v, not a list with an item %d", path[:i], v, k)
			}
			v = list[k]
		}
	}
	return v
}

// assertAt fails the test unless the value at path in v is want.
func assertAt(t *testing.T, v any, want any, path ...any) {
	t.Helper()
	if got := at(t, v, path...); got != want {
		t.Fatalf("%v = %#v, want %#v", path, got, want)
	}
}

func TestInfoJSONNamesTheProjectWorktreeAndURL(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-basic")
	d.MustRunCLI(repo.Dir, "link")

	info := infoJSON(t, d, repo.Dir)

	assertAt(t, info, 1.0, "schemaVersion")
	assertAt(t, info, "info-basic", "project", "name")
	assertAt(t, info, repo.Dir, "project", "path")
	assertAt(t, info, "", "worktree", "name")
	assertAt(t, info, repo.Dir, "worktree", "path")
	assertAt(t, info, true, "worktree", "main")
	assertAt(t, info, "info-basic.localhost", "domain")
	assertAt(t, info, "https://info-basic.localhost/", "url")
}

// assertLen fails the test unless the value at path in v is a list of n
// items.
func assertLen(t *testing.T, v any, n int, path ...any) {
	t.Helper()
	list, ok := at(t, v, path...).([]any)
	if !ok || len(list) != n {
		t.Fatalf("%v = %#v, want a list of %d", path, at(t, v, path...), n)
	}
}

// infoProcess is the item of info's processes called name.
func infoProcess(t *testing.T, info map[string]any, name string) map[string]any {
	t.Helper()
	for _, p := range at(t, info, "processes").([]any) {
		if p := p.(map[string]any); p["name"] == name {
			return p
		}
	}
	t.Fatalf("info has no Process %q: %v", name, info["processes"])
	return nil
}

func TestInfoJSONListsEveryDeclaredProcessWithItsStateAndURL(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-procs")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	info := infoJSON(t, d, repo.Dir)
	assertAt(t, info, "stopped", "state")
	var names []any
	for _, p := range at(t, info, "processes").([]any) {
		names = append(names, at(t, p, "name"))
	}
	if len(names) != 3 || names[0] != "queue" || names[1] != "vite" || names[2] != "web" {
		t.Fatalf("processes are %v, want queue, vite and web, sorted by name", names)
	}
	web := infoProcess(t, info, "web")
	assertAt(t, web, "stopped", "state")
	assertAt(t, web, "lazy", "startMode")
	assertAt(t, web, true, "primary")
	assertAt(t, web, true, "http")
	assertAt(t, web, "https://info-procs.localhost/", "url")
	vite := infoProcess(t, info, "vite")
	assertAt(t, vite, false, "primary")
	assertAt(t, vite, true, "http")
	assertAt(t, vite, "https://vite.info-procs.localhost/", "url")
	queue := infoProcess(t, info, "queue")
	assertAt(t, queue, false, "http")
	assertAt(t, queue, "", "url")

	startByBrowsing(t, d, "info-procs.localhost")
	waitForState(t, d, "info-procs.localhost", "running")

	info = infoJSON(t, d, repo.Dir)
	assertAt(t, info, "running", "state")
	for _, name := range []string{"queue", "vite", "web"} {
		assertAt(t, infoProcess(t, info, name), "running", "state")
	}
}

func TestInfoJSONListsTheWorktreesAliases(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-alias")
	d.MustRunCLI(repo.Dir, "link")
	assertLen(t, infoJSON(t, d, repo.Dir), 0, "aliases")

	d.MustRunCLI(repo.Dir, "worktree", "alias", "api")

	info := infoJSON(t, d, repo.Dir)
	assertLen(t, info, 1, "aliases")
	assertAt(t, info, "api.info-alias.localhost", "aliases", 0, "domain")
	assertAt(t, info, "https://api.info-alias.localhost/", "aliases", 0, "url")
}

func TestInfoJSONReportsSetupAndWhyItFailed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "info-setup").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList("echo checking >&2; exit 3")+"\n").
		Commit("declare a failing setup step")
	d.MustRunCLI(repo.Dir, "link")
	if res := d.RunCLI(repo.Dir, "worktree", "ready"); res.ExitCode != 1 {
		t.Fatalf("`portby worktree ready` exited %d, want 1 (setup failed):\n%s", res.ExitCode, res.Output())
	}

	info := infoJSON(t, d, repo.Dir)
	assertAt(t, info, "failed", "setup", "state")
	assertAt(t, info, "on_create: echo checking >&2; exit 3", "setup", "step")
	assertAt(t, info, "exit status 3", "setup", "error")
	assertAt(t, info, "checking", "setup", "output", 0)
	assertLen(t, info, 0, "setup", "warnings")
}

// infoService is the item of info's services called name.
func infoService(t *testing.T, info map[string]any, name string) map[string]any {
	t.Helper()
	for _, s := range at(t, info, "services").([]any) {
		if s := s.(map[string]any); s["name"] == name {
			return s
		}
	}
	t.Fatalf("info has no Service %q: %v", name, info["services"])
	return nil
}

func TestInfoJSONGivesEachWorktreesDatabasesAndHowToReachItsServices(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "info-db").
		WriteFile("portby.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n\n[services]\nredis = \"7\"\n").
		Commit("declare a Postgres database with Test Databases, and Redis")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	main := infoJSON(t, d, repo.Dir)
	assertAt(t, main, "postgres", "database", "engine")
	assertAt(t, main, "info_db", "database", "name")
	assertAt(t, main, "info_db_test", "database", "test")
	assertAt(t, main, "postgres:18", "database", "service")

	info := infoJSON(t, d, wt.Dir)
	assertAt(t, info, "feature", "worktree", "name")
	assertAt(t, info, wt.Dir, "worktree", "path")
	assertAt(t, info, false, "worktree", "main")
	assertAt(t, info, "feature.info-db.localhost", "domain")
	assertAt(t, info, "info_db__feature", "database", "name")
	assertAt(t, info, "info_db__feature_test", "database", "test")

	assertLen(t, info, 2, "services")
	assertAt(t, info, "postgres:18", "services", 0, "name") // sorted by name
	pg := infoService(t, info, "postgres:18")
	assertAt(t, pg, "postgres", "engine")
	assertAt(t, pg, "18", "version")
	assertAt(t, pg, d.ServiceHost(), "host")
	assertAt(t, pg, 5432.0, "port")
	assertAt(t, pg, "portby", "user")
	assertAt(t, pg, "portby", "password")
	assertAt(t, pg, "running", "state") // setup started it
	assertAt(t, pg, "", "error")
	redis := infoService(t, info, "redis:7")
	assertAt(t, redis, d.ServiceHost(), "host")
	assertAt(t, redis, 6379.0, "port")
	assertAt(t, redis, "", "user")
	assertAt(t, redis, "portby", "password")
	assertAt(t, redis, "stopped", "state") // nothing has connected
}

func TestInfoJSONGivesASQLiteDatabasesPathAndNullWithoutADatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "info-sqlite", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	info := infoJSON(t, d, repo.Dir)
	assertAt(t, info, "sqlite", "database", "engine")
	assertAt(t, info, sqliteFile(repo.Dir), "database", "name")
	assertAt(t, info, "", "database", "test")
	assertAt(t, info, "", "database", "service")
	assertLen(t, info, 0, "services")

	plain := gitfixture.New(t, "info-nodb")
	d.MustRunCLI(plain.Dir, "link")
	assertAt(t, infoJSON(t, d, plain.Dir), nil, "database")
}

func TestInfoJSONSaysWhyItCantListProcessesOfABrokenConfig(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-broken")
	d.MustRunCLI(repo.Dir, "link")
	assertAt(t, infoJSON(t, d, repo.Dir), "", "configError")
	writeTestFile(t, filepath.Join(repo.Dir, "portby.local.toml"), "[processes.web]\nstart_mode = \"sometimes\"\n")

	info := infoJSON(t, d, repo.Dir)
	assertContains(t, at(t, info, "configError").(string), "portby.local.toml")
	assertContains(t, at(t, info, "configError").(string), "start_mode")
	assertLen(t, info, 0, "processes")
	assertAt(t, info, "https://info-broken.localhost/", "url")
}

// infoSchema is `portby info --json`'s schema version 1, as documented for
// agents and scripts: every key of every object, with its JSON type
// ("object?" may be null). Adding a key means adding it here and to the
// documented contract; removing or renaming one, or changing its type,
// needs a new schemaVersion.
var infoSchema = map[string]map[string]string{
	"": {
		"schemaVersion": "number", "project": "object", "worktree": "object", "domain": "string",
		"url": "string", "aliases": "list", "setup": "object?", "state": "string", "processes": "list",
		"database": "object?", "services": "list", "configError": "string",
	},
	"project":   {"name": "string", "path": "string"},
	"worktree":  {"name": "string", "path": "string", "main": "bool"},
	"aliases":   {"domain": "string", "url": "string"},
	"setup":     {"state": "string", "step": "string", "error": "string", "output": "list", "warnings": "list"},
	"processes": {"name": "string", "startMode": "string", "primary": "bool", "http": "bool", "state": "string", "url": "string"},
	"database":  {"engine": "string", "name": "string", "test": "string", "service": "string"},
	"services": {
		"name": "string", "engine": "string", "version": "string", "host": "string", "port": "number",
		"user": "string", "password": "string", "state": "string", "error": "string",
	},
}

// jsonType is v's type in infoSchema's words.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// assertSchema fails the test unless obj has exactly the keys of
// infoSchema[name], each of its type, and so on for the objects in it.
func assertSchema(t *testing.T, name string, obj map[string]any) {
	t.Helper()
	keys := infoSchema[name]
	for key := range obj {
		if _, ok := keys[key]; !ok {
			t.Errorf("%q has the undocumented key %q", name, key)
		}
	}
	for key, want := range keys {
		v, ok := obj[key]
		if !ok {
			t.Errorf("%q has no key %q", name, key)
			continue
		}
		got := jsonType(v)
		if got != strings.TrimSuffix(want, "?") && !(got == "null" && strings.HasSuffix(want, "?")) {
			t.Errorf("%q's %q is %s (%v), want %s", name, key, got, v, want)
			continue
		}
		if _, nested := infoSchema[key]; !nested || name != "" {
			continue
		}
		switch v := v.(type) {
		case map[string]any:
			assertSchema(t, key, v)
		case []any:
			if len(v) == 0 {
				t.Errorf("%q is empty, so its items' keys aren't checked", key)
			}
			for _, item := range v {
				assertSchema(t, key, item.(map[string]any))
			}
		}
	}
}

func TestInfoJSONHasEveryDocumentedKeyAndNoOthers(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "info-schema").
		WriteFile("portby.toml", processConfig(server+" --name web")+
			"\n[database]\nengine = \"postgres\"\ntest = \"on\"\n\n[services]\nredis = \"7\"\n").
		Commit("declare everything info shows")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "api")

	assertSchema(t, "", infoJSON(t, d, repo.Dir))

	// Nothing but the Worktree: the lists are empty, not null, and the
	// objects that don't apply are null.
	plain := gitfixture.New(t, "info-schema-plain")
	d.MustRunCLI(plain.Dir, "link")
	info := infoJSON(t, d, plain.Dir)
	for _, list := range []string{"aliases", "processes", "services"} {
		assertLen(t, info, 0, list)
	}
	assertAt(t, info, nil, "database")
}

func TestInfoExitsWith3WhenPortbyDoesntServeTheDirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	unlinked := gitfixture.New(t, "info-unlinked")
	linked := gitfixture.New(t, "info-linked")
	d.MustRunCLI(linked.Dir, "link")

	assertExit3 := func(what, dir string) {
		t.Helper()
		res := d.RunCLI(dir, "info", "--json")
		if res.ExitCode != 3 || res.Stdout != "" || res.Stderr == "" {
			t.Fatalf("`portby info --json` %s exited %d, want 3 with only a message on stderr:\n%s", what, res.ExitCode, res.Output())
		}
	}
	assertExit3("outside a git repository", gitfixture.TempDir(t))
	assertExit3("in a repository that isn't Linked", unlinked.Dir)
	d.Stop()
	assertExit3("with no daemon running", linked.Dir)
}

// assertLineWith fails the test unless a line of out has all of fields (as
// whitespace-separated words), in that order.
func assertLineWith(t *testing.T, out string, fields ...string) {
	t.Helper()
	for line := range strings.Lines(out) {
		words, i := strings.Fields(line), 0
		for _, w := range words {
			if i < len(fields) && w == fields[i] {
				i++
			}
		}
		if i == len(fields) {
			return
		}
	}
	t.Fatalf("no line has %q, in order:\n%s", fields, out)
}

func TestInfoPrintsTheSameDetailsForPeople(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "info-text").
		WriteFile("portby.toml", processConfig(server+" --name web")+
			"\n[processes.vite]\ncommand = \""+server+" --name vite\"\nhttp = true\nstart_mode = \"manual\"\n"+
			"\n[database]\nengine = \"postgres\"\ntest = \"on\"\n\n[services]\nredis = \"7\"\n").
		Commit("declare everything info shows")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "api")

	out := d.MustRunCLI(wt.Dir, "info").Stdout

	assertLineWith(t, out, "Worktree", `"feature"`, "of", `"info-text"`)
	assertLineWith(t, out, "Path", wt.Dir)
	assertLineWith(t, out, "URL", "https://feature.info-text.localhost/")
	assertLineWith(t, out, "Aliases", "https://api.info-text.localhost/")
	assertLineWith(t, out, "Readiness", "ready")
	assertLineWith(t, out, "State", "stopped")
	assertLineWith(t, out, "vite", "manual", "stopped", "https://vite.feature.info-text.localhost/")
	assertLineWith(t, out, "web", "lazy", "stopped", "https://feature.info-text.localhost/", "(primary)")
	assertLineWith(t, out, "Database", "postgres", "info_text__feature", "on", "postgres:18")
	assertLineWith(t, out, "Test", "Database", "info_text__feature_test")
	assertLineWith(t, out, "postgres:18", d.ServiceHost()+":5432", "portby", "portby", "running")
	assertLineWith(t, out, "redis:7", d.ServiceHost()+":6379", "-", "portby", "stopped")
}

func TestInfoJSONsURLIsTheWorktreesPrimaryDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-primary")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(repo.Dir, "link")
	assertURL := func(want string) {
		t.Helper()
		info := infoJSON(t, d, wt.Dir)
		assertAt(t, info, want, "url")
		assertAt(t, info, "t3-298e5f25.info-primary.localhost", "domain")
	}
	assertURL("https://t3-298e5f25.info-primary.localhost/")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	assertURL("https://checkout.info-primary.localhost/")

	// The newest Alias wins, though it sorts first.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")
	assertURL("https://basket.info-primary.localhost/")
	assertAt(t, infoJSON(t, d, wt.Dir), "basket.info-primary.localhost", "aliases", 0, "domain")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "basket")
	assertURL("https://checkout.info-primary.localhost/")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")
	assertURL("https://t3-298e5f25.info-primary.localhost/")
}

func TestTheMainWorktreesPrimaryDomainSurvivesADaemonRestart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "info-primary-main")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "beta")

	d.Restart()

	info := infoJSON(t, d, repo.Dir)
	assertAt(t, info, "https://beta.info-primary-main.localhost/", "url")
	assertAt(t, info, "info-primary-main.localhost", "domain")

	d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "beta")
	assertAt(t, infoJSON(t, d, repo.Dir), "https://demo.info-primary-main.localhost/", "url")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "demo")
	assertAt(t, infoJSON(t, d, repo.Dir), "https://info-primary-main.localhost/", "url")
}
