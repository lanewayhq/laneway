package runtimes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Dialect is a version constraint syntax. Composer's and npm's mostly agree;
// they differ on `~1.2`, on a bare `1.2` and on `>1.2` (see Satisfies).
type Dialect int

const (
	// Composer is composer.json's syntax (require.php).
	Composer Dialect = iota
	// NPM is package.json's syntax (engines.node), node-semver's ranges.
	NPM
)

// version is a release version: major.minor.patch. Pre-release and build
// suffixes are ignored, so "8.5.0RC1" is 8.5.0.
type version [3]int

func (v version) compare(w version) int {
	for i := range v {
		if v[i] != w[i] {
			if v[i] < w[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// partial is a version as written in a constraint: up to three numbers, any
// of which may be a wildcard (x, X, *), and n says how many were given.
type partial struct {
	v version
	n int // numbers given before the first wildcard (0 to 3)
}

var leadingDigits = regexp.MustCompile(`^\d+`)

// parseVersion reads a version such as "8.4.12", "v22.11.0" or "8.5.0-dev".
func parseVersion(s string) (version, error) {
	p, err := parsePartial(s)
	if err != nil {
		return version{}, err
	}
	if p.n == 0 {
		return version{}, fmt.Errorf("%q is not a version", s)
	}
	return p.v, nil
}

func parsePartial(s string) (partial, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	var p partial
	if s == "" {
		return p, fmt.Errorf("missing version")
	}
	parts := strings.SplitN(s, ".", 3)
	for i, part := range parts {
		if part == "*" || part == "x" || part == "X" {
			return p, nil
		}
		digits := leadingDigits.FindString(part)
		if digits == "" {
			return p, fmt.Errorf("%q is not a version", s)
		}
		// Anything after the digits ("RC1", "-dev", "+build", "@stable")
		// may only follow the last number written.
		if digits != part && i < len(parts)-1 {
			return p, fmt.Errorf("%q is not a version", s)
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return p, fmt.Errorf("%q is not a version", s)
		}
		p.v[i] = n
		p.n = i + 1
		if digits != part {
			break
		}
	}
	return p, nil
}

// bump is the smallest version above every version matching p's first n
// numbers: bump(8.2, 2) = 8.3.0, bump(8.2, 1) = 9.0.0.
func bump(v version, n int) version {
	var b version
	copy(b[:], v[:n])
	b[n-1]++
	return b
}

// bound is one comparison: op is one of < <= > >= = !=.
type bound struct {
	op string
	v  version
}

func (b bound) allows(v version) bool {
	c := v.compare(b.v)
	switch b.op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "!=":
		return c != 0
	default:
		return c == 0
	}
}

// Satisfies reports whether version satisfies constraint, written in d's
// syntax: alternatives separated by `||` (Composer also accepts `|`), each a
// set of conditions that must all hold, separated by spaces (Composer also
// accepts commas). A condition is a hyphen range `1.2 - 2.0`, a wildcard
// `8.*` / `8.x`, `^1.2`, `~1.2`, or a comparison with < <= > >= = == != (or
// none). Composer's `@stability` flags are ignored. Where the dialects
// differ: `~1.2` is <2.0.0 for Composer and <1.3.0 for npm; a bare `1.2` is
// exactly 1.2.0 for Composer and 1.2.x for npm; and npm reads `>1.2` as
// >=1.3.0 and `<=1.2` as <1.3.0.
func Satisfies(versionString, constraint string, d Dialect) (bool, error) {
	v, err := parseVersion(versionString)
	if err != nil {
		return false, err
	}
	alternatives, err := parseConstraint(constraint, d)
	if err != nil {
		return false, err
	}
	for _, all := range alternatives {
		ok := true
		for _, b := range all {
			ok = ok && b.allows(v)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

var hyphenRange = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)$`)

// parseConstraint returns the alternatives, each the bounds that must all
// hold.
func parseConstraint(constraint string, d Dialect) ([][]bound, error) {
	s := strings.TrimSpace(constraint)
	if s == "" {
		return nil, fmt.Errorf("empty constraint")
	}
	var alts []string
	if d == Composer {
		alts = strings.Split(strings.ReplaceAll(s, "||", "|"), "|")
	} else {
		alts = strings.Split(s, "||")
	}
	var out [][]bound
	for _, alt := range alts {
		alt = strings.TrimSpace(alt)
		if d == Composer {
			alt = strings.ReplaceAll(alt, ",", " ")
		}
		if alt == "" {
			if d == NPM {
				out = append(out, nil) // npm's "" is any version
				continue
			}
			return nil, fmt.Errorf("empty alternative in %q", constraint)
		}
		var bounds []bound
		if m := hyphenRange.FindStringSubmatch(alt); m != nil {
			from, err := parsePartial(stripStability(m[1]))
			if err != nil {
				return nil, err
			}
			to, err := parsePartial(stripStability(m[2]))
			if err != nil {
				return nil, err
			}
			if from.n > 0 {
				bounds = append(bounds, bound{">=", from.v})
			}
			if to.n == 3 {
				bounds = append(bounds, bound{"<=", to.v})
			} else if to.n > 0 {
				bounds = append(bounds, bound{"<", bump(to.v, to.n)})
			}
			out = append(out, bounds)
			continue
		}
		for _, term := range joinOperators(strings.Fields(alt)) {
			b, err := parseTerm(stripStability(term), d)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", constraint, err)
			}
			bounds = append(bounds, b...)
		}
		out = append(out, bounds)
	}
	return out, nil
}

// joinOperators glues an operator written apart from its version (">= 8.2")
// back onto it.
func joinOperators(fields []string) []string {
	var terms []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.Trim(f, "<>=!^~") == "" && i+1 < len(fields) {
			f += fields[i+1]
			i++
		}
		terms = append(terms, f)
	}
	return terms
}

// stripStability drops Composer's "@dev"-style flag.
func stripStability(term string) string {
	if i := strings.IndexByte(term, '@'); i >= 0 {
		return term[:i]
	}
	return term
}

var operator = regexp.MustCompile(`^(<=|>=|==|!=|<|>|=|\^|~)?`)

func parseTerm(term string, d Dialect) ([]bound, error) {
	op := operator.FindString(term)
	p, err := parsePartial(term[len(op):])
	if err != nil {
		return nil, err
	}
	if p.n == 0 { // "*", "x", ">=*" …: any version
		if op == "<" || op == "!=" {
			return nil, fmt.Errorf("%q matches no version", term)
		}
		return nil, nil
	}
	lowest := p.v
	switch op {
	case "^":
		// Up to the next change of the first non-zero number written.
		n := p.n
		for i := 0; i < p.n; i++ {
			if p.v[i] != 0 || i == p.n-1 {
				n = i + 1
				break
			}
		}
		return []bound{{">=", lowest}, {"<", bump(p.v, n)}}, nil
	case "~":
		n := p.n - 1
		if d == NPM || p.n == 1 {
			n = min(p.n, 2)
		}
		return []bound{{">=", lowest}, {"<", bump(p.v, n)}}, nil
	case "", "=", "==":
		if p.n < 3 && (d == NPM || isWildcard(term)) {
			return []bound{{">=", lowest}, {"<", bump(p.v, p.n)}}, nil
		}
		return []bound{{"=", lowest}}, nil
	case ">":
		if d == NPM && p.n < 3 {
			return []bound{{">=", bump(p.v, p.n)}}, nil
		}
	case "<=":
		if d == NPM && p.n < 3 {
			return []bound{{"<", bump(p.v, p.n)}}, nil
		}
	}
	return []bound{{op, lowest}}, nil
}

// isWildcard reports whether a term ends in a wildcard: "8.2.*", "8.x".
func isWildcard(term string) bool {
	return strings.HasSuffix(term, "*") || strings.HasSuffix(term, "x") || strings.HasSuffix(term, "X")
}

var firstVersion = regexp.MustCompile(`\d+(\.\d+)?`)

// suggestedVersion is what to ask mise for to satisfy constraint: the first
// major.minor it mentions, or "latest".
func suggestedVersion(constraint string) string {
	if v := firstVersion.FindString(constraint); v != "" {
		return v
	}
	return "latest"
}

// CheckConstraint checks that constraint can be read in d's syntax (see
// Satisfies).
func CheckConstraint(constraint string, d Dialect) error {
	_, err := parseConstraint(constraint, d)
	return err
}
