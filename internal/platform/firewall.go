package platform

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// wslVMCreatorID is the VMCreatorId Windows' Hyper-V firewall knows WSL by.
const wslVMCreatorID = "{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}"

// webPorts are the ports other devices reach Laneway on.
var webPorts = []int{80, 443}

func (l *Linux) WSLFirewall(ctx context.Context) (WSLFirewall, error) {
	if !l.WSL {
		return WSLFirewall{}, errors.New("Windows' firewall only stands between WSL2 and other devices")
	}
	// The active store is what Windows enforces: its own settings merged
	// with Group Policy's. Neither cmdlet needs an administrator.
	out, err := l.powershell(ctx, "$id = '"+wslVMCreatorID+"'; "+
		"$s = Get-NetFirewallHyperVVMSetting -PolicyStore ActiveStore -Name $id -ErrorAction Stop; "+
		`"setting $($s.Enabled) $($s.DefaultInboundAction)"; `+
		"Get-NetFirewallHyperVRule -PolicyStore ActiveStore -VMCreatorId $id -Direction Inbound -ErrorAction SilentlyContinue | "+
		`ForEach-Object { "rule $($_.Enabled) $($_.Action) $($_.Protocol) $($_.LocalPorts -join ',')" }; `+
		// No rules is a (silenced) error, which would make the exit status 1.
		"exit 0")
	if err != nil {
		return WSLFirewall{}, fmt.Errorf("asking Windows about its Hyper-V firewall: %w", err)
	}
	return parseWSLFirewall(out)
}

// parseWSLFirewall reads what WSLFirewall's PowerShell prints: a line
// "setting <Enabled> <DefaultInboundAction>", then a line
// "rule <Enabled> <Action> <Protocol> <LocalPorts>" for each inbound rule.
func parseWSLFirewall(out string) (WSLFirewall, error) {
	var enabled, inbound string
	allowed, blocked := map[int]bool{}, map[int]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "setting":
			enabled, inbound = f[1], f[2]
		case len(f) == 5 && f[0] == "rule" && f[1] == "True" && (f[3] == "TCP" || f[3] == "6" || f[3] == "Any"):
			for _, port := range webPorts {
				if !portsInclude(f[4], port) {
					continue
				}
				switch f[2] {
				case "Allow":
					allowed[port] = true
				case "Block":
					blocked[port] = true
				}
			}
		}
	}
	if enabled == "" {
		return WSLFirewall{}, fmt.Errorf("Windows didn't say how its Hyper-V firewall treats WSL: %q", strings.TrimSpace(out))
	}
	if enabled == "False" {
		return WSLFirewall{Open: true, Detail: "Windows' Hyper-V firewall is off for WSL"}, nil
	}
	var byRule, byDefault []string
	for _, port := range webPorts {
		switch {
		case blocked[port]:
			byRule = append(byRule, strconv.Itoa(port))
		case !allowed[port] && inbound != "Allow":
			byDefault = append(byDefault, strconv.Itoa(port))
		}
	}
	switch {
	case len(byRule) > 0:
		return WSLFirewall{Detail: "a Hyper-V firewall rule blocks other devices from WSL's port " + strings.Join(byRule, " and ")}, nil
	case len(byDefault) > 0:
		return WSLFirewall{Detail: "Windows' Hyper-V firewall blocks other devices from WSL's port " + strings.Join(byDefault, " and ") +
			" by default, and no rule allows them"}, nil
	case inbound == "Allow":
		return WSLFirewall{Open: true, Detail: "Windows' Hyper-V firewall lets other devices reach WSL by default"}, nil
	default:
		return WSLFirewall{Open: true, Detail: "Hyper-V firewall rules let other devices reach WSL's ports 80 and 443"}, nil
	}
}

// portsInclude reports whether a rule's LocalPorts, such as "Any", "443"
// or "80,8000-8100", include port.
func portsInclude(ports string, port int) bool {
	for p := range strings.SplitSeq(ports, ",") {
		if p == "Any" {
			return true
		}
		lo, hi, isRange := strings.Cut(p, "-")
		if !isRange {
			hi = lo
		}
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 == nil && err2 == nil && from <= port && port <= to {
			return true
		}
	}
	return false
}
