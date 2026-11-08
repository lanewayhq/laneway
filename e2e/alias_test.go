package e2e

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestAliasServesTheWorktreeUntilRemoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-basic")
	wt := repo.AddWorktree("t3-8f2c91")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(wt.Dir, "worktree", "alias", "api-v2")
	assertContains(t, res.Stdout, "api-v2.alias-basic.localhost")

	if got := d.GetServer("https://api-v2.alias-basic.localhost/"); got.Name != "web@t3-8f2c91" {
		t.Fatalf("api-v2.alias-basic.localhost reached %q, want the t3-8f2c91 Worktree's Primary Process", got.Name)
	}
	// The Worktree's own Domain still works.
	if got := d.GetServer("https://t3-8f2c91.alias-basic.localhost/"); got.Name != "web@t3-8f2c91" {
		t.Fatalf("t3-8f2c91.alias-basic.localhost reached %q", got.Name)
	}

	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "api-v2")

	assertNotServed(t, d, "https://api-v2.alias-basic.localhost/")
}

func TestAliasSaysWhereTheWorktreesURLMoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-moved")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	assertContains(t, res.Stdout, `Added the Alias checkout.alias-moved.localhost to Worktree "t3-298e5f25"; `+
		"its URL is now https://checkout.alias-moved.localhost/")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")

	// checkout isn't the Primary Domain any more, so the URL stays.
	res = d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")
	assertContains(t, res.Stdout, `Removed the Alias checkout.alias-moved.localhost from Worktree "t3-298e5f25"`)
	assertNotContains(t, res.Stdout, "URL is now")

	res = d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "basket")
	assertContains(t, res.Stdout, `Removed the Alias basket.alias-moved.localhost from Worktree "t3-298e5f25"; `+
		"its URL is now https://t3-298e5f25.alias-moved.localhost/")

	// The same for the main Worktree.
	res = d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	assertContains(t, res.Stdout, "the main Worktree; its URL is now https://demo.alias-moved.localhost/")
	res = d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "demo")
	assertContains(t, res.Stdout, "the main Worktree; its URL is now https://alias-moved.localhost/")
}

func TestAddingAnAliasTheWorktreeHasSaysSoAndWhereItsURLIs(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-again")
	wt := repo.AddWorktree("t3-5c0a77e1")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")

	// Adding checkout again changes nothing: basket stays the Primary Domain.
	res := d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	assertContains(t, res.Stdout, `checkout.alias-again.localhost is already an Alias of Worktree "t3-5c0a77e1"; `+
		"its URL is https://basket.alias-again.localhost/")
	assertNotContains(t, res.Stdout, "Added")
	if got := infoJSON(t, d, wt.Dir)["url"]; got != "https://basket.alias-again.localhost/" {
		t.Fatalf("the Worktree's URL is %v after adding checkout again, want basket's", got)
	}
}

func TestAliasesAreUniqueWithinAProject(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-unique")
	first := repo.AddWorktree("first")
	second := repo.AddWorktree("second")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(first.Dir, "worktree", "alias", "api-v2")

	res := d.RunCLI(second.Dir, "worktree", "alias", "api-v2")
	if res.ExitCode == 0 {
		t.Fatalf("a second Worktree took the Alias api-v2:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "api-v2.alias-unique.localhost is already an Alias of Worktree \"first\"")
	// Another Worktree's own Domain can't be taken either.
	res = d.RunCLI(repo.Dir, "worktree", "alias", "second")
	if res.ExitCode == 0 {
		t.Fatalf("the main Worktree took Worktree second's Domain as an Alias:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "second.alias-unique.localhost is already the Domain of Worktree \"second\"")

	if got := d.GetServer("https://api-v2.alias-unique.localhost/"); got.Name != "web@first" {
		t.Fatalf("api-v2.alias-unique.localhost reached %q, want Worktree first", got.Name)
	}
	if got := d.GetServer("https://second.alias-unique.localhost/"); got.Name != "web@second" {
		t.Fatalf("second.alias-unique.localhost reached %q, want Worktree second", got.Name)
	}
}

func TestAliasNameMustBeAHostnameLabel(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-invalid")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "worktree", "alias", "API_v2")

	if res.ExitCode == 0 {
		t.Fatalf("API_v2 was accepted as an Alias:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `such as "api-v2"`)
}

func TestAliasesSurviveADaemonRestart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-restart")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "stable")

	d.Restart()

	// The first request after the restart goes to an Alias: it alone has to
	// get the Worktree going.
	if got := d.GetServer("https://demo.alias-restart.localhost/"); got.Name != "web@feature" {
		t.Fatalf("demo.alias-restart.localhost reached %q after a restart, want Worktree feature", got.Name)
	}
	if got := d.GetServer("https://stable.alias-restart.localhost/"); got.Name != "web@alias-restart" {
		t.Fatalf("stable.alias-restart.localhost reached %q after a restart, want the main Worktree", got.Name)
	}
	info, err := d.Client().Worktree(t.Context(), wt.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Worktree.Aliases; len(got) != 1 || got[0] != "demo.alias-restart.localhost" {
		t.Fatalf("Worktree feature's Aliases = %q, want [demo.alias-restart.localhost]", got)
	}
}

func TestHTTPProcessesAreServedUnderAliasesAndWorktreeDomains(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-proc")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	// Each is the first request for its Domain.
	for url, want := range map[string]string{
		"https://vite.feature.alias-proc.localhost/": "vite@feature",
		"https://vite.demo.alias-proc.localhost/":    "vite@feature",
	} {
		if got := d.GetServer(url); got.Name != want {
			t.Errorf("%s reached %q, want %s", url, got.Name, want)
		}
	}
}

func TestAProcessNameIsKeptForTheMainWorktreesProcess(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-clash")
	d.MustRunCLI(repo.Dir, "link")

	// A Worktree named like the main Worktree's vite Process gets another
	// label, so vite.alias-clash.localhost stays the vite Process.
	repo.AddWorktree("vite")
	got := waitForWorktree(t, d, "alias-clash", "vite")
	if got.Domain == "vite.alias-clash.localhost" {
		t.Fatalf("Worktree vite took vite.alias-clash.localhost from the vite Process")
	}
	if res := d.GetServer("https://" + got.Domain + "/"); res.Name != "web@vite" {
		t.Fatalf("%s reached %q, want Worktree vite", got.Domain, res.Name)
	}
	if res := d.GetServer("https://vite.alias-clash.localhost/"); res.Name != "vite@alias-clash" {
		t.Fatalf("vite.alias-clash.localhost reached %q, want the main Worktree's vite Process", res.Name)
	}

	// And an Alias can't take it.
	res := d.RunCLI(repo.Dir, "worktree", "alias", "vite")
	if res.ExitCode == 0 {
		t.Fatalf("vite was accepted as an Alias:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `the main Worktree's "vite" Process`)
}

func TestARemovedWorktreesAliasesGoWithIt(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-removed")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	repo.Git("worktree", "remove", wt.Dir)
	waitForNoWorktree(t, d, "alias-removed", "feature")

	assertNotServed(t, d, "https://demo.alias-removed.localhost/")
	// The name is free again.
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	if got := d.GetServer("https://demo.alias-removed.localhost/"); got.Name != "web@alias-removed" {
		t.Fatalf("demo.alias-removed.localhost reached %q, want the main Worktree", got.Name)
	}
}

func TestRemovingAnAliasTheWorktreeDoesNotHaveFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-unknown")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	res := d.RunCLI(repo.Dir, "worktree", "alias", "demo", "--remove")

	if res.ExitCode == 0 {
		t.Fatalf("the main Worktree removed Worktree feature's Alias:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "the main Worktree has no Alias demo.alias-unknown.localhost")
	if got := d.GetServer("https://demo.alias-unknown.localhost/"); got.Name != "web@feature" {
		t.Fatalf("demo.alias-unknown.localhost reached %q, want Worktree feature", got.Name)
	}
}

func TestAVisitToAnAliasOrAProcessStartsTheWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-lazy")
	other := repo.AddWorktree("other")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	// Until then a visit gets the Preparing page.
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.MustRunCLI(other.Dir, "worktree", "ready")

	for url, want := range map[string]string{
		"https://demo.alias-lazy.localhost/":       "web@alias-lazy",
		"https://vite.other.alias-lazy.localhost/": "vite@other",
	} {
		got := serverResponse(t, url, d.Navigate(url))
		if got.Name != want {
			t.Fatalf("%s reached %q, want %s", url, got.Name, want)
		}
	}
}

func TestAMistypedNameUnderADomainStartsNothing(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "alias-typo")
	d.MustRunCLI(repo.Dir, "link")

	assertNotServed(t, d, "https://vtie.alias-typo.localhost/")
	assertNotServed(t, d, "https://queue.alias-typo.localhost/")

	projects, err := d.Client().Projects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if procs := projects[0].Worktrees[0].Processes; len(procs) != 0 {
		t.Fatalf("the main Worktree's Processes started: %+v", procs)
	}
}

// serverResponse decodes page, from url, as the test server's answer,
// failing the test if it is anything else.
func serverResponse(t *testing.T, url string, page testkit.Response) testkit.ServerResponse {
	t.Helper()
	var res testkit.ServerResponse
	if page.StatusCode != http.StatusOK || json.Unmarshal([]byte(page.Body), &res) != nil {
		t.Fatalf("visiting %s: status %d, want the test server:\n%s", url, page.StatusCode, page.Body)
	}
	return res
}
