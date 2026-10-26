package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// envProject is a repo that ignores .env files, as Laravel's does, and
// declares the Env Patch templates in portby.toml.
func envProject(t *testing.T, name, env string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n.env.portby.bak\n").
		WriteFile("portby.toml", "[env]\n"+env).
		Commit("declare the Env Patch")
}

const handWrittenEnv = `# Laravel env
APP_NAME="My Shop"
APP_URL=http://localhost   # set by hand

export DB_DATABASE=laravel
PRIVATE_KEY="-----BEGIN-----
abc
-----END-----"
`

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	if got := readFile(t, path); got != want {
		t.Fatalf("%s =\n%s\nwant\n%s", path, got, want)
	}
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists (err %v), want no such file", path, err)
	}
}

func TestLinkWithYesPatchesOnlyTheKeysPortbyOwnsAndBacksUpTheOriginal(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-yes", `APP_URL = "{url}"
CACHE_PREFIX = "{project}_"
`).WriteFile(".env", handWrittenEnv)

	res := d.MustRunCLI(repo.Dir, "link", "--yes")

	assertFile(t, filepath.Join(repo.Dir, ".env"), `# Laravel env
APP_NAME="My Shop"
APP_URL=https://env-yes.localhost   # set by hand

export DB_DATABASE=laravel
PRIVATE_KEY="-----BEGIN-----
abc
-----END-----"
CACHE_PREFIX=env-yes_
`)
	assertFile(t, filepath.Join(repo.Dir, ".env.portby.bak"), handWrittenEnv)
	assertContains(t, res.Stdout, "- APP_URL=http://localhost")
	assertContains(t, res.Stdout, "+ APP_URL=https://env-yes.localhost")
	assertContains(t, res.Stdout, "+ CACHE_PREFIX=env-yes_")
	assertContains(t, res.Stdout, `Linked Project "env-yes"`)
}

func TestLinkShowsTheDiffAndChangesNothingUnlessConfirmed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-confirm", `APP_URL = "{url}"`+"\n").WriteFile(".env", handWrittenEnv)

	declined := d.RunCLIWithInput(repo.Dir, "n\n", "link")

	if declined.ExitCode == 0 {
		t.Fatalf("declining exited 0:\n%s", declined.Output())
	}
	assertContains(t, declined.Stdout, "Env Patch for the main Worktree: "+filepath.Join(repo.Dir, ".env"))
	assertContains(t, declined.Stdout, "- APP_URL=http://localhost")
	assertContains(t, declined.Stdout, "+ APP_URL=https://env-confirm.localhost")
	assertContains(t, declined.Stdout, "Apply these changes? [y/N]")
	assertContains(t, declined.Stderr, "--yes")
	assertFile(t, filepath.Join(repo.Dir, ".env"), handWrittenEnv)
	assertNoFile(t, filepath.Join(repo.Dir, ".env.portby.bak"))
	assertNotContains(t, d.MustRunCLI(repo.Dir, "status").Stdout, "env-confirm")

	// No answer at all (stdin isn't a terminal) is no, too.
	if res := d.RunCLI(repo.Dir, "link"); res.ExitCode == 0 {
		t.Fatalf("link with no answer exited 0:\n%s", res.Output())
	}
	assertFile(t, filepath.Join(repo.Dir, ".env"), handWrittenEnv)

	confirmed := d.RunCLIWithInput(repo.Dir, "y\n", "link")

	if confirmed.ExitCode != 0 {
		t.Fatalf("confirming exited %d:\n%s", confirmed.ExitCode, confirmed.Output())
	}
	assertContains(t, confirmed.Stdout, `Linked Project "env-confirm"`)
	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "APP_URL=https://env-confirm.localhost   # set by hand\n")
	assertFile(t, filepath.Join(repo.Dir, ".env.portby.bak"), handWrittenEnv)
}

func TestLinkDoesNotAskWhenTheEnvPatchIsAlreadyInPlace(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	patched := "APP_URL=https://env-in-place.localhost\n"
	repo := envProject(t, "env-in-place", `APP_URL = "{url}"`+"\n").WriteFile(".env", patched)

	res := d.MustRunCLI(repo.Dir, "link")

	assertNotContains(t, res.Stdout, "Apply these changes?")
	assertFile(t, filepath.Join(repo.Dir, ".env"), patched)
	assertNoFile(t, filepath.Join(repo.Dir, ".env.portby.bak"))
}

func TestWithoutEnvTemplatesLinkLeavesEnvFilesAlone(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "env-none").WriteFile(".env.example", "APP_URL=http://localhost\n").Commit("example")

	d.MustRunCLI(repo.Dir, "link")

	assertNoFile(t, filepath.Join(repo.Dir, ".env"))
}

func TestWithoutAnEnvFileLinkCopiesEnvExampleAndPatchesIt(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	example := "APP_NAME=Shop\nAPP_URL=http://localhost\n"
	repo := envProject(t, "env-example", `APP_URL = "{url}"`+"\n").
		WriteFile(".env.example", example).
		Commit("add .env.example")

	res := d.RunCLIWithInput(repo.Dir, "y\n", "link")

	assertContains(t, res.Stdout, "(new file, copied from "+filepath.Join(repo.Dir, ".env.example")+")")
	assertFile(t, filepath.Join(repo.Dir, ".env"), "APP_NAME=Shop\nAPP_URL=https://env-example.localhost\n")
	assertFile(t, filepath.Join(repo.Dir, ".env.example"), example)
	assertNoFile(t, filepath.Join(repo.Dir, ".env.portby.bak"))
}

func TestANewWorktreeGetsACopyOfTheMainEnvWithItsOwnEnvPatch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-wt", `APP_URL = "{url}"
CACHE_PREFIX = "{project}_{worktree}_"
`).WriteFile(".env", "APP_KEY=base64:secret\nAPP_URL=http://localhost\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	wt := repo.AddWorktree("feature-x")
	waitForWorktree(t, d, "env-wt", "feature-x")

	assertFile(t, filepath.Join(wt.Dir, ".env"),
		"APP_KEY=base64:secret\nAPP_URL=https://feature-x.env-wt.localhost\nCACHE_PREFIX=env-wt_feature-x_\n")
	assertNoFile(t, filepath.Join(wt.Dir, ".env.portby.bak"))
	assertFile(t, filepath.Join(repo.Dir, ".env"),
		"APP_KEY=base64:secret\nAPP_URL=https://env-wt.localhost\nCACHE_PREFIX=env-wt__\n")
}

func TestLinkShowsAndAppliesTheEnvPatchOfWorktreesThatAlreadyExist(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-existing", `APP_URL = "{url}"`+"\n").WriteFile(".env", "APP_URL=http://localhost\n")
	wt := repo.AddWorktree("feature-y")

	res := d.RunCLIWithInput(wt.Dir, "y\n", "link")

	assertContains(t, res.Stdout, `Env Patch for Worktree "feature-y": `+filepath.Join(wt.Dir, ".env"))
	assertContains(t, res.Stdout, "(new file, copied from "+filepath.Join(repo.Dir, ".env")+")")
	assertContains(t, res.Stdout, "+ APP_URL=https://feature-y.env-existing.localhost")
	assertFile(t, filepath.Join(wt.Dir, ".env"), "APP_URL=https://feature-y.env-existing.localhost\n")
	assertFile(t, filepath.Join(repo.Dir, ".env"), "APP_URL=https://env-existing.localhost\n")
}

func TestTheEnvPatchIsReappliedWhenItsTemplatesChange(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-reapply", `APP_URL = "{url}"`+"\n").WriteFile(".env", handWrittenEnv)
	wt := repo.AddWorktree("feature-z")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	repo.WriteFile("portby.local.toml", "[env]\nAPP_URL = \"{url}/shop\"\nSESSION_DOMAIN = \"{domain}\"\n")
	wt.WriteFile("portby.local.toml", "[env]\nAPP_URL = \"{url}/shop\"\n")
	d.Restart()

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "APP_URL=https://env-reapply.localhost/shop   # set by hand\n")
	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "SESSION_DOMAIN=env-reapply.localhost\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "APP_URL=https://feature-z.env-reapply.localhost/shop   # set by hand\n")
	// The backup is still the file from before Portby's first patch.
	assertFile(t, filepath.Join(repo.Dir, ".env.portby.bak"), handWrittenEnv)

	// Re-applying a patch that is in place changes nothing.
	before := readFile(t, filepath.Join(repo.Dir, ".env"))
	res := d.MustRunCLI(repo.Dir, "link")
	assertNotContains(t, res.Stdout, "Env Patch for")
	assertFile(t, filepath.Join(repo.Dir, ".env"), before)
}

func TestLinkRefusesAnEnvTemplateWithAnUnknownVariable(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-bad", `APP_URL = "https://{domian}"`+"\n").WriteFile(".env", handWrittenEnv)

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("link exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(repo.Dir, "portby.toml"))
	assertContains(t, res.Stderr, "env.APP_URL")
	assertContains(t, res.Stderr, "{domian}")
	assertFile(t, filepath.Join(repo.Dir, ".env"), handWrittenEnv)
	assertNotContains(t, d.MustRunCLI(repo.Dir, "status").Stdout, "env-bad")
}

func TestTheEnvPatchWritesThroughASymlinkedEnvAndKeepsItsPermissions(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-symlink", `APP_URL = "{url}"`+"\n")
	shared := filepath.Join(gitfixture.TempDir(t), "shared.env")
	if err := os.WriteFile(shared, []byte("APP_URL=http://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(repo.Dir, ".env")); err != nil {
		t.Fatal(err)
	}

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if target, err := os.Readlink(filepath.Join(repo.Dir, ".env")); err != nil || target != shared {
		t.Fatalf(".env is no longer a symlink to %s (target %q, err %v)", shared, target, err)
	}
	assertFile(t, shared, "APP_URL=https://env-symlink.localhost\n")
	if info, err := os.Stat(shared); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after patch = %v (err %v), want 0600", info.Mode().Perm(), err)
	}
	assertFile(t, filepath.Join(repo.Dir, ".env.portby.bak"), "APP_URL=http://localhost\n")
}
