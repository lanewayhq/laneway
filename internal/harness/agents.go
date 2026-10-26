package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const (
	agentsFile = "AGENTS.md"
	claudeFile = "CLAUDE.md"
)

// agentsBlock is what every generator puts between the markers in AGENTS.md.
// It is static: the Worktree's own details come from `portby info --json`.
const agentsBlock = `## Local environment (Portby)

This repository is served by Portby when it's installed. Every git worktree gets its own HTTPS URL, database and ` + "`.env`" + `, so work only with this worktree's.

- Find this worktree's URL, its Processes and their states, its databases and its Service connection details with ` + "`portby info --json`" + `. Use that URL rather than starting your own server or guessing a port.
- Run the project's commands (tests, artisan, composer, npm…) with ` + "`portby exec <command>`" + `, such as ` + "`portby exec php artisan test`" + `, so they use this worktree's PHP and Node versions.
- ` + "`portby worktree ready`" + ` waits until this worktree is set up. If it reports that setup failed, show the user its output; ` + "`portby worktree ready --retry`" + ` runs setup again once the problem is fixed.
- If there is no ` + "`portby`" + ` command, Portby isn't installed: ignore this section.
`

// claudeImport is the marked block added to CLAUDE.md: Claude Code reads
// CLAUDE.md, not AGENTS.md, and `@path` on its own line imports a file.
const claudeImport = "@" + agentsFile + "\n"

// importsAgents matches an existing import of AGENTS.md in CLAUDE.md.
var importsAgents = regexp.MustCompile(`(^|\s)@(\./)?AGENTS\.md(\s|$)`)

// agentsEdit adds Portby's block to AGENTS.md, creating the file if needed.
func agentsEdit(root string) (edit, error) {
	return markdownEdit(root, agentsFile, agentsBlock)
}

// claudeImportEdit adds a marked import of AGENTS.md to CLAUDE.md. It returns
// false when there is nothing to do: no CLAUDE.md (Claude Code then reads
// AGENTS.md itself), CLAUDE.md is AGENTS.md (a link), or CLAUDE.md already
// imports it.
func claudeImportEdit(root string) (edit, bool, error) {
	old, err := readFile(root, claudeFile)
	if err != nil || old == nil {
		return edit{}, false, err
	}
	if sameFile(filepath.Join(root, claudeFile), filepath.Join(root, agentsFile)) {
		return edit{}, false, nil
	}
	if begin, _, err := findBlock(string(old)); err == nil && begin < 0 && importsAgents.Match(old) {
		return edit{}, false, nil
	}
	e, err := markdownEdit(root, claudeFile, claudeImport)
	return e, true, err
}

// markdownEdit merges body into the marked block of the file rel.
func markdownEdit(root, rel, body string) (edit, error) {
	old, err := readFile(root, rel)
	if err != nil {
		return edit{}, err
	}
	doc, err := mergeBlock(string(old), body)
	if err != nil {
		return edit{}, fmt.Errorf("%s: %w", rel, err)
	}
	return edit{path: rel, old: old, new: []byte(doc)}, nil
}

func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}
