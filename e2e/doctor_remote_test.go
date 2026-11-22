package e2e

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// Doctor's checks for reaching Worktrees from other devices (ADR-0009).

func TestDoctorWithNeitherRootDomainNorListenSaysOnlyThisMachineReachesWorktrees(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetWSL2()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "ok", "remote access", "off: only this machine reaches Worktrees")
	for _, check := range []string{"listen", "DNS", "Windows firewall", "reachable from"} {
		assertNoCheck(t, out, check)
	}
}

// assertNoCheck asserts doctor printed no line for check, or for a check
// whose name starts with check and a space, such as "listen tailscale".
func assertNoCheck(t *testing.T, out, check string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		_, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, check+":") || strings.HasPrefix(rest, check+" ") {
			t.Fatalf("doctor printed a %q check:\n%s", check, out)
		}
	}
}

func TestDoctorReportsEachListenAddressAndWhyItIsntListenedOn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetNoTailscale("Tailscale is stopped.")
	// 192.0.2.1 is reserved for documentation, so no machine has it.
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`", "tailscale", "192.0.2.1"]`+"\n")
	d.Restart()

	res := d.RunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "ok", "listen "+extraAddr, "listened on")
	assertCheck(t, res.Stdout, "FAIL", "listen tailscale", "no address: Tailscale is stopped.")
	assertContains(t, res.Stdout, "fix: start Tailscale and log in (`tailscale up`)")
	assertCheck(t, res.Stdout, "FAIL", "listen 192.0.2.1", "not listened on: can't listen on 192.0.2.1")
	if res.ExitCode == 0 {
		t.Fatalf("doctor exited 0 with listen addresses down:\n%s", res.Stdout)
	}
}

func TestDoctorNamesTheTailscaleAddressItListensOn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetTailscaleIPv4("127.0.0.3")
	writeGlobalConfig(t, d, `listen = ["tailscale"]`+"\n")
	d.Restart()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "ok", "listen tailscale", "127.0.0.3 is listened on")
}

func TestDoctorWarnsWhenAListenAddressHasNoPlainHTTP(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// Something else holds the plain HTTP port at the extra address, so
	// only HTTPS is listened on there.
	_, port, _ := net.SplitHostPort(d.HTTPAddr())
	taken, err := net.Listen("tcp", net.JoinHostPort(extraAddr, port))
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	testkit.WaitUntil(t, 10*time.Second, "the daemon listens on "+extraAddr, func() bool {
		info, err := d.Client().Daemon(t.Context())
		return err == nil && len(info.Listen) == 1 && info.Listen[0].Listening
	})

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "warn", "listen "+extraAddr, "listened on for HTTPS, but not plain HTTP")
	assertContains(t, out, "the CA page")
}

func TestDoctorFailsWhenListenCantBeRead(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["wireguard"]`+"\n")
	d.Restart()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "FAIL", "listen", `"wireguard" is neither an IP address nor "tailscale"`)
}

func TestDoctorSaysToInstallTailscaleWhenListenNamesItAndItIsntInstalled(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["tailscale"]`+"\n")
	d.Restart()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "FAIL", "listen tailscale", "no address: Tailscale isn't installed")
	assertContains(t, out, "fix: install Tailscale (https://tailscale.com/download) and log in; Laneway listens once it has an address\n")
}

// remoteDaemon starts a daemon whose personal config sets root as the Root
// Domain and listens on listen beside loopback.
func remoteDaemon(t *testing.T, root string, listen ...string) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	config := `root_domain = "` + root + `"` + "\n"
	if len(listen) > 0 {
		config += `listen = ["` + strings.Join(listen, `", "`) + `"]` + "\n"
	}
	writeGlobalConfig(t, d, config)
	d.Restart()
	return d
}

func TestDoctorSaysWhenNamesUnderTheRootDomainResolveToWhereLanewayListens(t *testing.T) {
	t.Parallel()
	d := remoteDaemon(t, "dr-dns-ok.example.test", extraAddr)
	d.Platform.SetHost("*.dr-dns-ok.example.test", extraAddr)

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "ok", "DNS", "names under dr-dns-ok.example.test resolve to "+extraAddr+", where Laneway listens")
}

func TestDoctorFailsWhenNamesUnderTheRootDomainDontReachLaneway(t *testing.T) {
	t.Parallel()
	d := remoteDaemon(t, "dr-dns-fail.example.test", extraAddr)

	for what, setup := range map[string]func(){
		"no record": func() {},
		"elsewhere": func() { d.Platform.SetHost("*.dr-dns-fail.example.test", "203.0.113.9") },
		"only the Dashboard's name": func() {
			d.Platform.SetHost("*.dr-dns-fail.example.test")
			d.Platform.SetHost("laneway.dr-dns-fail.example.test", extraAddr)
		},
	} {
		setup()

		res := d.RunCLI(d.Home, "doctor")

		if res.ExitCode == 0 {
			t.Fatalf("%s: doctor exited 0:\n%s", what, res.Stdout)
		}
		assertCheck(t, res.Stdout, "FAIL", "DNS", "lane-check-")
		assertContains(t, res.Stdout, "fix: create the record *.dr-dns-fail.example.test A "+extraAddr)
	}
}

func TestDoctorWarnsWhenNamesUnderTheRootDomainResolveWhereOtherDevicesCantReachLaneway(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ root, record, want string }{
		{"dr-dns-loopback.example.test", "127.0.0.1", "resolve to 127.0.0.1, which is loopback: only this machine reaches Laneway there"},
		{"dr-dns-down.example.test", "192.0.2.1", "resolve to 192.0.2.1, where Laneway isn't listening yet"},
	} {
		// 192.0.2.1 is reserved for documentation, so it can't be listened on.
		d := remoteDaemon(t, tc.root, extraAddr, "192.0.2.1")
		d.Platform.SetHost("*."+tc.root, tc.record)

		out := d.RunCLI(d.Home, "doctor").Stdout

		assertCheck(t, out, "warn", "DNS", tc.want)
	}
}

func TestDoctorOnWSL2ListeningBeyondLoopbackQuotesTheWindowsFirewallRule(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetWSL2()
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	d.Restart()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "warn", "Windows firewall", "Hyper-V firewall may block other devices from ports 80 and 443")
	assertContains(t, out, `New-NetFirewallHyperVRule -Name Laneway -DisplayName "Laneway (HTTP and HTTPS)" -Direction Inbound `+
		`-VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 80,443`)
}

func TestDoctorOnLinuxListeningBeyondLoopbackHasNoWindowsFirewallHint(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	d.Restart()

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertNoCheck(t, out, "Windows firewall")
}

func TestDoctorSaysEverythingIsReachableFromTheAddressesLanewayListensOn(t *testing.T) {
	t.Parallel()
	d := remoteDaemon(t, "dr-reach.example.test", extraAddr, "192.0.2.1")
	d.Platform.SetHost("*.dr-reach.example.test", extraAddr)

	out := d.RunCLI(d.Home, "doctor").Stdout

	assertCheck(t, out, "ok", "reachable from", extraAddr+": every Worktree, the Dashboard and the Service Domains")
	if line := checkLine(out, "reachable from"); strings.Contains(line, "192.0.2.1") {
		t.Fatalf("the statement names an address Laneway doesn't listen on:\n%s", line)
	}
	assertNoCheck(t, out, "remote access")
}

func TestDoctorWarnsAboutAHalfDoneSetupForOtherDevices(t *testing.T) {
	t.Parallel()

	t.Run("listening with the Root Domain still localhost", func(t *testing.T) {
		t.Parallel()
		d := testkit.StartDaemon(t)
		writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
		d.Restart()

		out := d.RunCLI(d.Home, "doctor").Stdout

		assertCheck(t, out, "warn", "remote access", "Laneway listens on "+extraAddr+", but the Root Domain is localhost")
		assertContains(t, out, "fix: set a Root Domain with `lane root-domain set <domain>`")
		// Other devices still reach every name, so it still says so.
		assertCheck(t, out, "ok", "reachable from", extraAddr)
	})

	t.Run("a custom Root Domain listened on only on loopback", func(t *testing.T) {
		t.Parallel()
		d := remoteDaemon(t, "dr-half.example.test")
		d.Platform.SetHost("*.dr-half.example.test", "127.0.0.1")

		out := d.RunCLI(d.Home, "doctor").Stdout

		assertCheck(t, out, "warn", "remote access", "the Root Domain is dr-half.example.test, but Laneway listens only on loopback")
		assertContains(t, out, `listen = ["tailscale"]`)
		assertNoCheck(t, out, "reachable from")
	})
}

// checkLine is doctor's line for check, or "".
func checkLine(out, check string) string {
	for line := range strings.SplitSeq(out, "\n") {
		_, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		if strings.HasPrefix(strings.TrimSpace(rest), check+":") {
			return line
		}
	}
	return ""
}
