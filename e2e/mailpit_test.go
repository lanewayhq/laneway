package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// dialMail connects to the Mailpit Service listening on port on the
// daemon's Service host, without reading anything.
func dialMail(t *testing.T, d *testkit.Daemon, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(d.ServiceHost(), strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("connecting to the Service on port %d: %v", port, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sendMail sends one message with subject over conn, a fresh connection to
// an SMTP server, as an app's mailer would.
func sendMail(t *testing.T, conn net.Conn, subject string) {
	t.Helper()
	sendMailAs(t, conn, subject, nil)
}

// sendMailAs is sendMail logging in with auth first (nil = no login).
func sendMailAs(t *testing.T, conn net.Conn, subject string, auth smtp.Auth) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	c, err := smtp.NewClient(conn, "localhost")
	if err != nil {
		t.Fatalf("SMTP greeting: %v", err)
	}
	if err := c.Hello("app.localhost"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			t.Fatalf("AUTH: %v", err)
		}
	}
	if err := c.Mail("app@example.com"); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	if err := c.Rcpt("user@example.com"); err != nil {
		t.Fatalf("RCPT TO: %v", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	fmt.Fprintf(w, "From: app@example.com\r\nTo: user@example.com\r\nSubject: %s\r\n\r\nHello from the app.\r\n", subject)
	if err := w.Close(); err != nil {
		t.Fatalf("sending the message: %v", err)
	}
	if err := c.Quit(); err != nil {
		t.Fatalf("QUIT: %v", err)
	}
}

func TestMailpitAcceptsSMTPOn1025AndStartsOnTheFirstConnection(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "mail-smtp", `mailpit = "v1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("linking started a container: %v", calls)
	}

	release := d.Engine.HoldStarts()
	conn := dialMail(t, d, 1025)
	eventually(t, "the container to be started", func() bool { return containsCall(d.Engine.Calls(), "start") })
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, _ := conn.Read(make([]byte, 1)); n > 0 {
		t.Fatal("got the SMTP greeting before the container was ready")
	}
	conn.SetReadDeadline(time.Time{})
	release()

	sendMail(t, conn, "Welcome")
	if n := countCalls(d.Engine.Calls(), "start"); n != 1 {
		t.Fatalf("the container was started %d times, want once: %v", n, d.Engine.Calls())
	}
	if state := serviceState(t, d, "mailpit", "v1"); state != "running" {
		t.Fatalf("mailpit:v1 is %s, want running", state)
	}
}

// inboxSubjects are the subjects of the messages Mailpit's inbox at inbox
// (such as https://mail.laneway.localhost) lists, read through its API as
// the inbox's page does.
func inboxSubjects(t *testing.T, d *testkit.Daemon, inbox string) []string {
	t.Helper()
	res := d.Get(inbox + "/api/v1/messages")
	var list struct{ Messages []struct{ Subject string } }
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &list) != nil {
		t.Fatalf("GET %s/api/v1/messages: want 200 with Mailpit's message list, got %d:\n%s", inbox, res.StatusCode, res.Body)
	}
	var subjects []string
	for _, m := range list.Messages {
		subjects = append(subjects, m.Subject)
	}
	return subjects
}

func TestTheInboxIsAtMailLanewayLocalhostAndVisitingItStartsMailpit(t *testing.T) {
	t.Parallel()
	// The only test that may use mail.laneway.localhost: Caddy's cert cache
	// is shared by name (see dashboardDaemon).
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "mail-inbox", `mailpit = "v1"`, "").Dir, "link")

	if subjects := inboxSubjects(t, d, "https://mail.laneway.localhost"); len(subjects) != 0 {
		t.Fatalf("a new inbox lists %q", subjects)
	}
	if n := countCalls(d.Engine.Calls(), "start"); n != 1 {
		t.Fatalf("visiting the inbox started the container %d times, want once: %v", n, d.Engine.Calls())
	}
	if state := serviceState(t, d, "mailpit", "v1"); state != "running" {
		t.Fatalf("mailpit:v1 is %s after visiting the inbox, want running", state)
	}

	sendMail(t, dialMail(t, d, 1025), "Your order shipped")
	if subjects := inboxSubjects(t, d, "https://mail.laneway.localhost"); len(subjects) != 1 || subjects[0] != "Your order shipped" {
		t.Fatalf("the inbox lists %q, want the message just sent", subjects)
	}
}

func TestMailHostAndPortAreEnvPatchVariables(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "mail-env", `mailpit = "v1"`,
		"[env]\nMAIL_HOST = \"{mailpit_host}\"\nMAIL_PORT = \"{mailpit_port}\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"MAIL_HOST=" + d.ServiceHost(), "MAIL_PORT=1025"} {
		assertContains(t, env, line+"\n")
	}
	// What the Env Patch says is what works.
	sendMail(t, dialMail(t, d, 1025), "Hello")
}

func TestTheInboxExplainsHowToDeclareMailpitWhenNoProjectUsesIt(t *testing.T) {
	t.Parallel()
	d, _ := dashboardDaemon(t, "mail-undeclared")
	// Installed, as after a Project that used it was unlinked.
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.FirstPartyPlugin(t, "mailpit"))
	d.MustRunCLI(serviceProject(t, "mail-undeclared-redis", `redis = "7"`, "").Dir, "link")

	res := d.Get("https://mail.mail-undeclared.localhost/")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET the inbox with no Mailpit Service = %d, want 502:\n%s", res.StatusCode, res.Body)
	}
	assertContains(t, res.Body, "no Project uses Mailpit yet")
	assertContains(t, res.Body, "mailpit = &#34;v1&#34;")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("visiting the inbox started a container: %v", calls)
	}
}

func TestOnAFreshInstallTheInboxSaysToDeclareTheEngineServingItInAProject(t *testing.T) {
	t.Parallel()
	d, _ := dashboardDaemon(t, "mail-fresh")

	res := d.Get("https://mail.mail-fresh.localhost/")

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET the inbox with no Mailpit Plugin = %d, want 502:\n%s", res.StatusCode, res.Body)
	}
	// Until the Mailpit Plugin is fetched, no Engine manifest says that
	// mail is its Service Domain (the Catalog lists Engines' names only).
	assertContains(t, res.Body, "nothing is served at mail.mail-fresh.localhost yet")
	assertContains(t, res.Body, "Declare the Engine that does in the [services] table")
	assertContains(t, res.Body, "Linking it fetches its Plugin from the Catalog")
}
