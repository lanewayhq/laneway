package main

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"

	"github.com/lanewayhq/laneway/sdk"
)

// procfileDev is the Procfile bin/dev runs a Rails app's development
// Processes from (through foreman), such as CSS and JS watchers.
const procfileDev = "Procfile.dev"

// procfileLine matches a Procfile entry, the way foreman reads it.
var procfileLine = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.+)$`)

// serverCommand matches a command running the Rails server, which the
// Recipe's web Process already is.
var serverCommand = regexp.MustCompile(`\brails\s+(server|s)\b`)

// tailwindWatch matches tailwindcss-rails' watcher without arguments. It
// stops when its stdin closes, as a Process's does at once; [always] keeps it
// watching.
var tailwindWatch = regexp.MustCompile(`\btailwindcss:watch\b(\[[^\]]*\])?`)

// procfileProcesses adds every Procfile.dev entry but the web server as a
// Process of its own name. Like foreman, which loads .env for them, each
// reads the Worktree's .env.
func procfileProcesses(l *sdk.Layer, p sdk.Project) {
	data, err := p.ReadFile(procfileDev)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		m := procfileLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		name, command := m[1], strings.TrimSpace(m[2])
		if name == "web" || serverCommand.MatchString(command) {
			continue
		}
		command = tailwindWatch.ReplaceAllStringFunc(command, func(watch string) string {
			if strings.HasSuffix(watch, "]") {
				return watch
			}
			return watch + "[always]"
		})
		l.Processes[name] = sdk.Process{Command: withEnv + command}
	}
}
