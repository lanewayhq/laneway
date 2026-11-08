package testkit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// systemTools are the commands from the real machine that Process commands
// and stubs in tests may use. Tests get these and d.Bin on PATH, nothing
// else, so a real mise, PHP or Node on the machine is never found.
var systemTools = []string{
	"sh", "awk", "basename", "cat", "chmod", "cp", "cut", "date", "dirname", "echo",
	"env", "expr", "false", "find", "git", "grep", "head", "id", "kill", "ln", "ls",
	"mkdir", "mktemp", "mv", "printf", "pwd", "readlink", "realpath", "rm", "sed",
	"seq", "setsid", "sleep", "sort", "stat", "tail", "tee", "test", "touch", "tr", "true",
	"uniq", "wc", "xargs",
}

// systemBin makes a directory of links to systemTools in dir.
func systemBin(t testing.TB, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "system-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	for _, tool := range systemTools {
		path, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("testkit: %v", err)
		}
	}
	return bin
}

// WriteExecutable writes a shell script named name into dir (created if
// needed) and returns its path.
func WriteExecutable(t testing.TB, dir, name, script string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	return path
}

// StubPHP writes a fake `php` into dir. Asked for its version and
// extensions the way Laneway asks (`php -r …`), it reports version and
// extensions (as PHP names them, such as "Zend OPcache"). Run any other way,
// it prints "PHP <version> (stub) <args>".
func StubPHP(t testing.TB, dir, version string, extensions ...string) string {
	t.Helper()
	info := strings.Join(append([]string{version}, extensions...), "\n")
	return WriteExecutable(t, dir, "php", fmt.Sprintf(
		"if [ \"$1\" = -r ]; then printf '%%s\\n' %s; exit 0; fi\necho \"PHP %s (stub) $*\"\n",
		shellQuote(info), version))
}

// StubNode writes a fake `node` into dir that reports version (such as
// "v22.11.0") for --version, and otherwise prints "Node <version> (stub)
// <args>".
func StubNode(t testing.TB, dir, version string) string {
	t.Helper()
	return WriteExecutable(t, dir, "node", fmt.Sprintf(
		"if [ \"$1\" = --version ]; then echo %s; exit 0; fi\necho \"Node %s (stub) $*\"\n",
		shellQuote(version), version))
}

// StubMise writes a fake `mise` into dir. `mise env --json` prints env as
// JSON and, for each of missing (such as "php@8.4.1"), mise's "missing"
// warning on stderr; `mise --version` prints a version.
func StubMise(t testing.TB, dir string, env map[string]string, missing ...string) string {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	script := "if [ \"$1\" = --version ]; then echo '2026.10.4 linux-x64 (stub)'; exit 0; fi\n" +
		"if [ \"$1\" = env ]; then\n"
	if len(missing) > 0 {
		script += fmt.Sprintf("  echo %s >&2\n", shellQuote("mise WARN  missing: "+strings.Join(missing, " ")))
	}
	script += fmt.Sprintf("  echo %s\n  exit 0\nfi\necho \"mise (stub): unexpected $*\" >&2\nexit 2\n", shellQuote(string(data)))
	return WriteExecutable(t, dir, "mise", script)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
