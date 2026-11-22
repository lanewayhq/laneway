package platform

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestTailscaleIPv4IsWhatTheTailscaleCLIReports(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "tailscale ip -4" {
			return "100.101.102.103\n", nil
		}
		return "", errors.New("unexpected command")
	}

	got, err := m.linux().TailscaleIPv4(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("100.101.102.103"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestTailscaleIPv4OnWSLAsksTailscaleOnWindows(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "tailscale.exe ip -4" {
			return "100.64.0.7\r\n", nil
		}
		return "", errors.New("unexpected command")
	}

	got, err := m.wsl().TailscaleIPv4(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("100.64.0.7"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestTailscaleIPv4SaysWhyThereIsNone(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		m := newMachine(t)
		m.run.missing["tailscale"] = true
		_, err := m.linux().TailscaleIPv4(t.Context())
		if err == nil || !strings.Contains(err.Error(), "isn't installed") {
			t.Fatalf("err = %v, want it to say Tailscale isn't installed", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		m := newMachine(t)
		m.run.respond = func(Cmd) (string, error) {
			return "", errors.New("tailscale: exit status 1: Tailscale is stopped.")
		}
		_, err := m.linux().TailscaleIPv4(t.Context())
		if err == nil || !strings.Contains(err.Error(), "Tailscale is stopped.") {
			t.Fatalf("err = %v, want Tailscale's own reason", err)
		}
	})
}
