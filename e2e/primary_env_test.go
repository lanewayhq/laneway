package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// eventuallyContains waits until the file at path contains want: Portby
// re-applies the Env Patch for a moved Primary Domain in the background.
func eventuallyContains(t *testing.T, path, want string) {
	t.Helper()
	eventually(t, path+" to contain "+want, func() bool {
		return strings.Contains(readFile(t, path), want)
	})
}

func TestTheEnvPatchFollowsTheWorktreesPrimaryDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "primary-env", `APP_URL = "{url}"
SESSION_DOMAIN = "{domain}"
`).WriteFile(".env", "APP_URL=http://localhost\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	env := filepath.Join(wt.Dir, ".env")

	// Added second, basket is the newest Alias though it sorts first.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	eventuallyContains(t, env, "APP_URL=https://checkout.primary-env.localhost\nSESSION_DOMAIN=checkout.primary-env.localhost\n")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")
	eventuallyContains(t, env, "APP_URL=https://basket.primary-env.localhost\nSESSION_DOMAIN=basket.primary-env.localhost\n")

	// Removing the newest falls back to the previous Alias, then to the Domain.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "basket")
	eventuallyContains(t, env, "APP_URL=https://checkout.primary-env.localhost\nSESSION_DOMAIN=checkout.primary-env.localhost\n")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")
	eventuallyContains(t, env, "APP_URL=https://t3-298e5f25.primary-env.localhost\nSESSION_DOMAIN=t3-298e5f25.primary-env.localhost\n")

	// The same for the main Worktree.
	mainEnv := filepath.Join(repo.Dir, ".env")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	eventuallyContains(t, mainEnv, "APP_URL=https://demo.primary-env.localhost\nSESSION_DOMAIN=demo.primary-env.localhost\n")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "demo")
	eventuallyContains(t, mainEnv, "APP_URL=https://primary-env.localhost\nSESSION_DOMAIN=primary-env.localhost\n")
}
