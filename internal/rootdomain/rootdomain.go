// Package rootdomain is the Root Domain (ADR-0009): the domain every name
// Laneway serves is under, and the names it gives directly under it.
package rootdomain

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Default is the Root Domain unless the developer sets their own.
const Default = "localhost"

// Dashboard is the Dashboard's Domain under root: laneway.<root>.
func Dashboard(root string) string {
	return "laneway." + root
}

// Covers reports whether host, lowercase and without a port or trailing
// dot, is root itself or a name under it.
func Covers(root, host string) bool {
	return host == root || strings.HasSuffix(host, "."+root)
}

// IsDefault reports whether root is Default, localhost.
func IsDefault(root string) bool {
	return root == Default
}

// Move is name, a name under from or from itself, with the same labels
// under to instead: Move("vite.shop.localhost", "localhost",
// "lw.example.com") is "vite.shop.lw.example.com".
func Move(name, from, to string) string {
	return strings.TrimSuffix(name, from) + to
}

// MaxLen is the longest a Root Domain may be: a Process host,
// <process>.<worktree>.<project>.<root>, with labels of up to 63 characters
// each, must still fit the 253 characters DNS allows a name.
const MaxLen = 253 - 3*(63+1)

// Check returns why root can't be a Root Domain, or nil if it can: a
// lowercase hostname of one or more labels of letters, digits and hyphens
// (not starting or ending with a hyphen, at most 63 characters each), at
// most MaxLen characters, with no scheme, port, path or trailing dot, not
// an IP address and not a name under localhost. It doesn't have to be a
// registrable domain: dev.internal is fine.
func Check(root string) error {
	switch {
	case root == "":
		return errors.New("is empty")
	case strings.Contains(root, "://"):
		return fmt.Errorf("%q has a scheme: give the domain alone, such as lw.example.com", root)
	case strings.ContainsAny(root, "/?#"):
		return fmt.Errorf("%q has a path: give the domain alone, such as lw.example.com", root)
	case strings.Contains(root, ":"):
		return fmt.Errorf("%q has a port: Laneway serves it on 443 and 80", root)
	case net.ParseIP(root) != nil:
		return fmt.Errorf("%q is an IP address, not a domain: name a domain whose wildcard record points at it", root)
	case strings.HasSuffix(root, "."):
		return fmt.Errorf("%q ends with a dot: leave it out", root)
	case root != strings.ToLower(root):
		return fmt.Errorf("%q isn't lowercase", root)
	case len(root) > MaxLen:
		return fmt.Errorf("%q is %d characters, and a Root Domain may be at most %d: "+
			"names such as <process>.<worktree>.<project>.<Root Domain> must fit the 253 characters DNS allows",
			root, len(root), MaxLen)
	case root != Default && Covers(Default, root):
		return fmt.Errorf("%q is under localhost, which every device resolves to itself: "+
			"use localhost itself, or a domain your other devices resolve to this machine", root)
	}
	for label := range strings.SplitSeq(root, ".") {
		if !validLabel.MatchString(label) {
			return fmt.Errorf("%q has an invalid label %q: labels are letters, digits and hyphens, "+
				"not starting or ending with a hyphen, at most 63 characters", root, label)
		}
	}
	return nil
}

var validLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NamesStale reports whether value (such as an env file's value) names a
// host under a Root Domain Laneway no longer serves: one under localhost
// when current isn't localhost, or under previous (unless it is "" or
// current). Only names under them count, not localhost or previous
// themselves, so http://localhost:8000 doesn't. When current is under
// the old one, names under current don't count either.
func NamesStale(value, current, previous string) bool {
	value = strings.ToLower(value)
	for _, old := range []string{Default, previous} {
		if old == "" || old == current {
			continue
		}
		for i := 0; ; {
			j := strings.Index(value[i:], "."+old)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+1+len(old)
			i = end
			if end < len(value) && (hostByte(value[end]) || value[end] == '.' && end+1 < len(value) && hostByte(value[end+1])) {
				continue // a longer name, such as shop.localhost.example.com
			}
			for start > 0 && (hostByte(value[start-1]) || value[start-1] == '.') {
				start--
			}
			if host := value[start:end]; !Covers(old, current) || !Covers(current, host) {
				return true
			}
		}
	}
	return false
}

// hostByte reports whether b may be in a hostname's label.
func hostByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-'
}
