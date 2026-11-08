package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestANewWorktreeGetsTheGitignoredFilesConfigSaysToCopyFromTheMainWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "copy-files").
		WriteFile(".gitignore", "/AGENTS.md\n/CLAUDE.md\n/.claude/*\n!/.claude/settings.json\n/docs/local.md\n").
		WriteFile("laneway.toml", "[worktree]\n"+`copy = ["AGENTS.md", "CLAUDE.md", ".claude/skills", "docs", "missing.md"]`+"\n").
		WriteFile(".claude/settings.json", "{}\n").
		WriteFile("docs/guide.md", "committed\n").
		Commit("config").
		WriteFile("AGENTS.md", "# Rules\n").
		WriteFile(".claude/skills/deploy/SKILL.md", "---\nname: deploy\n---\n").
		WriteFile(".claude/settings.local.json", "{}\n").
		WriteFile("docs/guide.md", "edited in main\n").
		WriteFile("docs/local.md", "mine\n")
	if err := os.Symlink("AGENTS.md", filepath.Join(repo.Dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, ".claude/skills/deploy/run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d.MustRunCLI(repo.Dir, "link")

	wt := repo.AddWorktree("feature")
	waitForWorktree(t, d, "copy-files", "feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, filepath.Join(wt.Dir, "AGENTS.md"), "# Rules\n")
	if target, err := os.Readlink(filepath.Join(wt.Dir, "CLAUDE.md")); err != nil || target != "AGENTS.md" {
		t.Errorf("CLAUDE.md isn't a link to AGENTS.md: %q, %v", target, err)
	}
	assertFile(t, filepath.Join(wt.Dir, ".claude/skills/deploy/SKILL.md"), "---\nname: deploy\n---\n")
	if info, err := os.Stat(filepath.Join(wt.Dir, ".claude/skills/deploy/run.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh didn't keep its mode: %v, %v", info, err)
	}
	// Only the listed paths are copied, and the Worktree's own files are kept.
	assertNoFile(t, filepath.Join(wt.Dir, ".claude/settings.local.json"))
	assertFile(t, filepath.Join(wt.Dir, "docs/guide.md"), "committed\n")
	assertFile(t, filepath.Join(wt.Dir, "docs/local.md"), "mine\n")
	assertNoFile(t, filepath.Join(wt.Dir, "missing.md"))
}
