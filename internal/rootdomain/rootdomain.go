// Package rootdomain is the Root Domain (ADR-0009): the domain every name
// Laneway serves is under, and the names it gives directly under it.
package rootdomain

import "strings"

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
