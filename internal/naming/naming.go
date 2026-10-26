// Package naming turns Project and Worktree names into identifiers that are
// safe to use in Domains and database names. The rule is the
// spec's "Database naming" rule: lowercase, every character other than a
// letter or digit becomes a separator, and names longer than the limit are
// cut short with a hash suffix so they stay unique.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxLabel is the longest a DNS label may be.
const MaxLabel = 63

// hashLen is how many hex digits of the hash a suffix carries.
const hashLen = 8

// DomainLabel makes name safe to use as one label of a Domain: lowercase
// letters, digits and '-', not starting or ending with '-', at most MaxLabel
// characters.
func DomainLabel(name string) string {
	return normalize(name, '-', MaxLabel)
}

// DomainLabelWithHash is DomainLabel with a hash of seed appended, for a name
// whose plain label is already in use. Different seeds give different labels.
func DomainLabelWithHash(name, seed string) string {
	return withHash(DomainLabel(name), seed, '-', MaxLabel)
}

// DatabaseName is the name of a Worktree's database in a database Service
// whose identifiers may be max characters long (Postgres 63, MySQL 64):
// <project> for the main Worktree (worktree ""), else
// <project>__<worktree>. Each part is lowercased with every character other
// than a letter or digit replaced by '_', and a name longer than max is cut
// short with a hash of the Project and Worktree names as its suffix, so it
// stays unique.
func DatabaseName(project, worktree string, max int) string {
	name := normalize(project, '_', max)
	if worktree == "" {
		return name
	}
	name += "__" + normalize(worktree, '_', max)
	if len(name) > max {
		return withHash(name, project+"\x00"+worktree, '_', max)
	}
	return name
}

// normalize lowercases name, replaces every character other than an ASCII
// letter or digit with sep, trims sep from both ends and caps the result at
// max characters. A name that had to be cut, or that has nothing usable left
// in it, gets a hash of the original name as a suffix.
func normalize(name string, sep byte, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(sep)
		}
	}
	s := strings.Trim(b.String(), string(sep))
	if s == "" || len(s) > max {
		return withHash(s, name, sep, max)
	}
	return s
}

// withHash appends sep and a hash of seed to s, cutting s short so the result
// fits in max characters.
func withHash(s, seed string, sep byte, max int) string {
	sum := sha256.Sum256([]byte(seed))
	suffix := hex.EncodeToString(sum[:])[:hashLen]
	if keep := max - hashLen - 1; len(s) > keep {
		s = s[:keep]
	}
	s = strings.TrimRight(s, string(sep))
	if s == "" {
		return suffix
	}
	return s + string(sep) + suffix
}
