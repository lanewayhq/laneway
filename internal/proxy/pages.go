package proxy

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// StartingRefresh is how often the starting page reloads itself, in seconds.
const StartingRefresh = 1

// isNavigation reports whether r is a browser loading a page, as opposed to
// a script, asset or API client. Browsers send Sec-Fetch-Mode on every
// request; for clients that don't, a GET that prefers HTML counts.
func isNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// writeStartingPage answers a browser navigation while the Domain's
// Processes start. The page reloads itself until the app answers instead.
func writeStartingPage(w http.ResponseWriter, domain string) {
	refresh := fmt.Sprint(StartingRefresh)
	w.Header().Set("Refresh", refresh)
	w.Header().Set("Retry-After", refresh)
	writePage(w, http.StatusServiceUnavailable, "Starting…",
		fmt.Sprintf("Portby is starting the Processes for <code>%s</code>.", html.EscapeString(domain)),
		"This page reloads by itself and shows the app as soon as it accepts connections.")
}

// writeStartErrorPage explains why the Domain's Primary Process didn't start.
func writeStartErrorPage(w http.ResponseWriter, domain string, e *StartError) {
	status := http.StatusBadGateway
	if e.TimedOut {
		status = http.StatusGatewayTimeout
	}
	paragraphs := []string{
		fmt.Sprintf("Portby couldn't start the Primary Process <code>%s</code> for <code>%s</code>:",
			html.EscapeString(e.Process), html.EscapeString(domain)),
		"<pre>" + html.EscapeString(e.Err.Error()) + "</pre>",
	}
	if e.Command != "" {
		paragraphs = append(paragraphs, "Command:", "<pre>"+html.EscapeString(e.Command)+"</pre>")
	}
	if len(e.Output) > 0 {
		paragraphs = append(paragraphs, fmt.Sprintf("Its last %d lines of output:", len(e.Output)),
			"<pre>"+html.EscapeString(strings.Join(e.Output, "\n"))+"\n</pre>")
	} else if e.Command != "" {
		paragraphs = append(paragraphs, "It printed no output.")
	}
	paragraphs = append(paragraphs, "Fix the problem, then reload this page to start it again.")
	writePage(w, status, "The app didn't start", paragraphs...)
}

// writeSettingUpPage is the starting page for a Domain whose Worktree is
// still being set up.
func writeSettingUpPage(w http.ResponseWriter, domain string) {
	refresh := fmt.Sprint(StartingRefresh)
	w.Header().Set("Refresh", refresh)
	w.Header().Set("Retry-After", refresh)
	writePage(w, http.StatusServiceUnavailable, "Setting up…",
		fmt.Sprintf("The Worktree served at <code>%s</code> is being set up. "+
			"Its Processes start once it is Ready.", html.EscapeString(domain)),
		"This page reloads by itself. Run <code>portby worktree ready</code> in the Worktree to wait for it there.")
}

// writeSetupErrorPage explains why the setup of the Domain's Worktree failed.
func writeSetupErrorPage(w http.ResponseWriter, domain string, e *SetupError) {
	paragraphs := []string{
		fmt.Sprintf("The setup of the Worktree served at <code>%s</code> failed at the step <code>%s</code>, "+
			"so its Processes weren't started:", html.EscapeString(domain), html.EscapeString(e.Step)),
		"<pre>" + html.EscapeString(e.Err.Error()) + "</pre>",
	}
	if len(e.Output) > 0 {
		paragraphs = append(paragraphs, fmt.Sprintf("Its last %d lines of output:", len(e.Output)),
			"<pre>"+html.EscapeString(strings.Join(e.Output, "\n"))+"\n</pre>")
	}
	paragraphs = append(paragraphs, "Fix the problem, then run <code>portby worktree ready --retry</code> "+
		"in the Worktree and reload this page.")
	writePage(w, http.StatusBadGateway, "Setup failed", paragraphs...)
}
