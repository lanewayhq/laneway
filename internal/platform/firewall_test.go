package platform

import (
	"errors"
	"strings"
	"testing"
)

func TestWSLFirewallAsksWindowsAboutWSLsHyperVFirewall(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		line := cmdline(c)
		if strings.HasPrefix(line, "powershell.exe") && strings.Contains(line, "Get-NetFirewallHyperVVMSetting -PolicyStore ActiveStore -Name $id") &&
			strings.Contains(line, wslVMCreatorID) {
			return "setting True Block\r\nrule True Allow TCP 80,443\r\n", nil
		}
		return "", errors.New("unexpected command")
	}

	got, err := m.wsl().WSLFirewall(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Open {
		t.Fatalf("got %+v, want open: a rule allows both ports", got)
	}
}

func TestWSLFirewallIsAnErrorOnPlainLinux(t *testing.T) {
	m := newMachine(t)
	if _, err := m.linux().WSLFirewall(t.Context()); err == nil {
		t.Fatal("got no error")
	}
}

func TestWSLFirewallIsOpenOrClosedByItsDefaultAndRules(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		open      bool
		detail    string
	}{
		{"allows by default", "setting True Allow\nrule True Allow Any Any\n", true, "lets other devices reach WSL by default"},
		{"off", "setting False Block\n", true, "is off for WSL"},
		{"blocks by default", "setting True Block\nrule True Allow UDP 5353\n", false, "port 80 and 443 by default, and no rule allows them"},
		{"one port allowed", "setting True Block\nrule True Allow TCP 443\n", false, "port 80 by default"},
		{"a range and Any protocol", "setting True Block\nrule True Allow Any 1-1000\n", true, "rules let other devices reach"},
		{"a disabled rule", "setting True Block\nrule False Allow TCP 80,443\n", false, "by default"},
		{"a blocking rule wins", "setting True Allow\nrule True Block TCP 443\n", false, "a Hyper-V firewall rule blocks other devices from WSL's port 443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWSLFirewall(tc.out)
			if err != nil {
				t.Fatal(err)
			}
			if got.Open != tc.open || !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("got %+v, want open %v and a detail with %q", got, tc.open, tc.detail)
			}
		})
	}
}

func TestWSLFirewallIsAnErrorWhenWindowsDoesntSay(t *testing.T) {
	if _, err := parseWSLFirewall("\r\n"); err == nil {
		t.Fatal("got no error")
	}
}
