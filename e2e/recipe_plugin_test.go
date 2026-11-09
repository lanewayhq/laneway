package e2e

import (
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// What a Plugin's Recipe answers besides its layer: its Test Database
// decision, warnings and test env file. The layers test Plugin's layered
// Recipe answers with the Project's layered.json as its layer and the rest of
// its answer from answer.json.

// answeringPlugin is the layers test Plugin as Plugin answers, adding only
// the layered Recipe.
func answeringPlugin(t *testing.T) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "layers"), `{"name": "answers", "version": "0.1.0", "api_version": 1,
  "recipes": [{"name": "layered", "detect": {"file": "layered.json"}}]}`)
}

func TestARecipePluginNamingATestEnvFileOutsideTheWorktreeFailsTheLink(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", answeringPlugin(t))
	repo := gitfixture.New(t, "escaping-app").
		WriteFile(".gitignore", ".env\n").
		WriteFile("layered.json", layerJSON(t, map[string]any{
			"processes": map[string]any{"web": map[string]any{"command": "sleep 60", "primary": true}},
			"database":  map[string]any{"engine": "postgres"},
		})).
		WriteFile("answer.json", `{"test_database": true, "test_env": {"file": "../outside.env", "layered": true}}`).
		Commit("an app whose Recipe points its tests outside the Worktree")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `the layered Recipe (Plugin answers): test_env.file "../outside.env" must be a file name in the Worktree, such as ".env.testing"`)
	assertNoFile(t, repo.Dir+"/../outside.env")
}

func TestARecipePluginsTestDatabaseWarningsAreShownByDoctor(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", answeringPlugin(t))
	repo := gitfixture.New(t, "warned-app").
		WriteFile(".gitignore", ".env\n").
		WriteFile("layered.json", layerJSON(t, map[string]any{
			"processes": map[string]any{"web": map[string]any{"command": "sleep 60", "primary": true}},
			"database":  map[string]any{"engine": "postgres"},
		})).
		WriteFile("answer.json", `{"test_database": true, "test_database_warnings": ["spec/db.rb hard-codes the database"]}`).
		Commit("an app whose tests bypass the Test Database")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "doctor")

	assertContains(t, res.Output(), "spec/db.rb hard-codes the database")
}
