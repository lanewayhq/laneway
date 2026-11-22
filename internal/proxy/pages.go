package proxy

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"

	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// PreparingRefresh is how often the Preparing page reloads itself, in
// seconds.
const PreparingRefresh = 1

// isNavigation reports whether r is a browser loading a page, as opposed to
// a script, asset or API client. Browsers send Sec-Fetch-Mode on every
// request; for clients that don't, a GET that prefers HTML counts.
func isNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// isPageLoad reports whether r is a browser loading a page it can be
// redirected away from: a navigation that is a GET. A form post navigates
// too, but redirecting it would resend its body to another origin.
func isPageLoad(r *http.Request) bool {
	return r.Method == http.MethodGet && isNavigation(r)
}

// redirectToPrimaryDomain sends a browser to the same path and query on
// primary, the Primary Domain of the Worktree it asked for (ADR 0006). The
// redirect is temporary and not cached, so removing the Alias undoes it.
func redirectToPrimaryDomain(w http.ResponseWriter, r *http.Request, primary string) {
	host := primary
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		host = net.JoinHostPort(primary, port)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
}

// writeLocalhost answers a request for domain, a .localhost name, while
// the Root Domain is root, another domain: .localhost names are no longer
// served there (ADR-0009). The name domain moved to is the same labels
// under root. A page load is shown a page linking the same path and query
// there, if Laneway serves it (serves; nil serves every name), and any
// other request gets an error naming that URL in one line of plain text.
// It is never a redirect, so a stale .localhost URL fails on the PC, where
// it can be fixed.
func writeLocalhost(w http.ResponseWriter, r *http.Request, domain, root string, serves func(string) bool) {
	moved := rootdomain.Move(domain, rootdomain.Default, root)
	known := serves == nil || serves(moved)
	host := moved
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		host = net.JoinHostPort(moved, port)
	}
	url := "https://" + host + r.URL.RequestURI()
	why := fmt.Sprintf("the Root Domain is %s, so Laneway doesn't serve .localhost names", root)
	if !isPageLoad(r) {
		w.Header().Set("Cache-Control", "no-store")
		if known {
			http.Error(w, fmt.Sprintf("Laneway serves this at %s now: %s.", url, why), http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf("Laneway serves nothing at %s: %s, and serves nothing at %s either.",
				domain, why, moved), http.StatusNotFound)
		}
		return
	}
	whyHTML := fmt.Sprintf("The Root Domain is <code>%s</code>, so Laneway doesn't serve <code>.localhost</code> names "+
		"such as <code>%s</code>.", html.EscapeString(root), html.EscapeString(domain))
	if !known {
		writePage(w, http.StatusNotFound, "Nothing is served here", whyHTML,
			fmt.Sprintf("Nothing is served at <code>%s</code> either. "+
				"Run <code>lane status</code> to see the Linked Projects and their Domains.", html.EscapeString(moved)))
		return
	}
	writePage(w, http.StatusNotFound, "This address has moved", whyHTML,
		fmt.Sprintf("This page is at <a href=\"%s\">%s</a>.", html.EscapeString(url), html.EscapeString(url)),
		"Update any bookmark, env file or code that still uses the old address.")
}

// writeStartErrorPage explains why the Domain's Primary Process didn't start,
// or isn't running after a crash.
func writeStartErrorPage(w http.ResponseWriter, domain string, e *StartError) {
	status := http.StatusBadGateway
	if e.TimedOut {
		status = http.StatusGatewayTimeout
	}
	title, what, retry := "The Primary Process didn't start", "Laneway couldn't start the Process",
		"Fix the problem, then reload this page to start it again."
	if e.Crashed {
		title, what, retry = "The Primary Process crashed", "The Process crashed",
			"Laneway restarts a crashed Process after a short wait, unless it has crashed too often: then fix the problem "+
				"and run <code>lane restart</code>. Reload this page once it is running again."
	}
	paragraphs := []string{
		fmt.Sprintf("%s <code>%s</code> serving <code>%s</code>:",
			what, html.EscapeString(e.Process), html.EscapeString(domain)),
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
	paragraphs = append(paragraphs, retry)
	writePage(w, status, title, paragraphs...)
}

// writePreparingPage answers a browser navigation while the Domain's
// Worktree is Preparing. The page reloads itself; once the Worktree is
// Ready, the reload is held until the Primary Process answers.
func writePreparingPage(w http.ResponseWriter, domain string) {
	refresh := fmt.Sprint(PreparingRefresh)
	w.Header().Set("Refresh", refresh)
	w.Header().Set("Retry-After", refresh)
	writePage(w, http.StatusServiceUnavailable, "Preparing…",
		fmt.Sprintf("The Worktree served at <code>%s</code> is being prepared. "+
			"Its Processes start once it is Ready.", html.EscapeString(domain)),
		"This page reloads by itself. Run <code>lane worktree ready</code> in the Worktree to wait for it there.")
}

// writePreparationErrorPage explains why preparing the Domain's Worktree
// failed.
func writePreparationErrorPage(w http.ResponseWriter, domain string, e *PreparationError) {
	paragraphs := []string{
		fmt.Sprintf("Preparing the Worktree served at <code>%s</code> failed at the step <code>%s</code>, "+
			"so its Processes weren't started:", html.EscapeString(domain), html.EscapeString(e.Step)),
		"<pre>" + html.EscapeString(e.Err.Error()) + "</pre>",
	}
	if len(e.Output) > 0 {
		paragraphs = append(paragraphs, fmt.Sprintf("Its last %d lines of output:", len(e.Output)),
			"<pre>"+html.EscapeString(strings.Join(e.Output, "\n"))+"\n</pre>")
	}
	paragraphs = append(paragraphs, "Fix the problem, then run <code>lane worktree ready --retry</code> "+
		"in the Worktree and reload this page.")
	writePage(w, http.StatusBadGateway, "Preparation failed", paragraphs...)
}
