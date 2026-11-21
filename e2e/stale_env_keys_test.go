package e2e

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// staleEnv is a hand-written .env whose Laneway-owned APP_URL is in the Env
// Patch, and whose other keys name hosts Laneway served or never served.
// The values are secrets as far as Laneway knows, so none may be shown.
const staleEnv = `APP_URL=http://localhost
VITE_WEBSITE_URL="https://glowyfly-website.localhost"
APP_KEY=base64:s3cr3t-app-key
LOCAL_API=http://localhost:8000
`

// staleEnvKeys are the {key, file} pairs of info's staleEnvKeys.
func staleEnvKeys(t *testing.T, info map[string]any) [][2]string {
	t.Helper()
	var got [][2]string
	for _, k := range at(t, info, "staleEnvKeys").([]any) {
		k := k.(map[string]any)
		got = append(got, [2]string{k["key"].(string), k["file"].(string)})
	}
	return got
}

func TestInfoListsEnvKeysOutsideTheEnvPatchThatNameLocalhostUnderACustomRootDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// LEGACY_HOST is in the Env Patch, so Laneway owns it even though its
	// value names .localhost.
	repo := envProject(t, "stale-info", `APP_URL = "{url}"`+"\n"+`LEGACY_HOST = "legacy.localhost"`+"\n").
		WriteFile(".env", staleEnv)
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.SetRootDomain("stale-info.example.test")

	got := staleEnvKeys(t, infoJSON(t, d, repo.Dir))
	if want := [][2]string{{"VITE_WEBSITE_URL", ".env"}}; !equalPairs(got, want) {
		t.Fatalf("staleEnvKeys = %v, want %v", got, want)
	}
	res := d.MustRunCLI(repo.Dir, "info", "--json")
	assertNotContains(t, res.Stdout, "glowyfly-website")
}

func TestInfoListsEnvKeysThatNameThePreviousRootDomainInEveryEnvFile(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "stale-prev-a.example.test")
	repo := envProject(t, "stale-prev", `APP_URL = "{url}"`+"\n").
		WriteFile(".env", "APP_URL=x\nASSET_URL=https://cdn.stale-prev.stale-prev-a.example.test/assets\n"+
			"MAIL_FROM=hi@stale-prev-a.example.test\n").
		WriteFile(".env.testing", "APP_URL=x\nWEBHOOK_URL='https://hooks.stale-prev-a.example.test/x'\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	if got := staleEnvKeys(t, infoJSON(t, d, repo.Dir)); len(got) != 0 {
		t.Fatalf("before the change, staleEnvKeys = %v, want none: everything names the current Root Domain", got)
	}

	d.SetRootDomain("stale-prev-b.example.test")

	got := staleEnvKeys(t, infoJSON(t, d, repo.Dir))
	want := [][2]string{{"ASSET_URL", ".env"}, {"WEBHOOK_URL", ".env.testing"}}
	if !equalPairs(got, want) {
		t.Fatalf("staleEnvKeys = %v, want %v", got, want)
	}
}

func TestNoEnvKeysAreListedUnderLocalhostWithNoPreviousRootDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "stale-none", `APP_URL = "{url}"`+"\n").WriteFile(".env", staleEnv)
	d.MustRunCLI(repo.Dir, "link", "--yes")

	if got := staleEnvKeys(t, infoJSON(t, d, repo.Dir)); len(got) != 0 {
		t.Fatalf("staleEnvKeys = %v, want none while the Root Domain is localhost", got)
	}
}

func TestTheDashboardIsToldWhichEnvKeysOfEachWorktreeAreStaleAsTheyChange(t *testing.T) {
	t.Parallel()
	// The Dashboard is pinned under the Root Domain set below: .localhost
	// names aren't served once the Root Domain is a custom one (#134).
	const dashDomain = "dash.stale-dash.example.test"
	d := testkit.StartDaemon(t, func(c *daemon.Config) { c.DashboardDomain = dashDomain })
	dash := "https://" + dashDomain
	repo := envProject(t, "stale-dash-shop", `APP_URL = "{url}"`+"\n").WriteFile(".env", staleEnv)
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.SetRootDomain("stale-dash.example.test")
	events := projectEvents(t, d, dash+"/api/v1/events")

	want := []api.StaleEnvKey{{Key: "VITE_WEBSITE_URL", File: ".env"}}
	waitForEvent(t, events, "both Worktrees' stale VITE_WEBSITE_URL", func(list api.ProjectList) bool {
		for _, domain := range []string{"stale-dash-shop.stale-dash.example.test", "feature.stale-dash-shop.stale-dash.example.test"} {
			if w, ok := worktreeIn(list, domain); !ok || !slices.Equal(w.StaleEnvKeys, want) {
				return false
			}
		}
		return true
	})

	// Fixing the value in the linked Worktree's .env clears its warning.
	env := filepath.Join(wt.Dir, ".env")
	writeFile(t, env, strings.ReplaceAll(readFile(t, env), "glowyfly-website.localhost", "glowyfly-website.stale-dash.example.test"))
	waitForEvent(t, events, "no stale keys in the fixed Worktree", func(list api.ProjectList) bool {
		w, ok := worktreeIn(list, "feature.stale-dash-shop.stale-dash.example.test")
		return ok && len(w.StaleEnvKeys) == 0
	})

	_, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/projects", "", nil)
	assertNotContains(t, body, "glowyfly-website")
}

func TestDoctorListsStaleEnvKeysByFileWithoutTheirValues(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "stale-doctor", `APP_URL = "{url}"`+"\n").
		WriteFile(".env", staleEnv+"ASSET_URL=https://cdn.stale-doctor.localhost\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	if out := d.RunCLI(repo.Dir, "doctor").Stdout; !strings.Contains(out, "stale env keys: none") {
		t.Fatalf("under localhost, doctor lists stale env keys, want none:\n%s", out)
	}

	d.SetRootDomain("stale-doctor.example.test")

	out := d.RunCLI(repo.Dir, "doctor").Stdout
	assertCheck(t, out, "warn", "stale env keys", filepath.Join(repo.Dir, ".env")+": VITE_WEBSITE_URL, ASSET_URL")
	assertNotContains(t, out, "glowyfly-website")
	assertNotContains(t, out, "cdn.stale-doctor")
}

func equalPairs(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
