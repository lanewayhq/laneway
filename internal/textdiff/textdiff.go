// Package textdiff shows how one text differs from another, line by line,
// as a unified diff: what `lane plugin test` prints when a Plugin returns
// something other than its golden file holds.
package textdiff

import (
	"fmt"
	"strings"
)

// context is how many unchanged lines are shown around each change.
const context = 3

// edit is one line of the diff: kept (' '), removed ('-') or added ('+'),
// and where it is in the old and new text (0-based, before this line).
type edit struct {
	kind   byte
	line   string
	at, to int
}

// Unified is the unified diff from old, called oldName, to new, called
// newName; "" if they are the same.
func Unified(oldName, newName, old, new string) string {
	a, b := lines(old), lines(new)
	edits := diff(a, b)
	var out strings.Builder
	for i := 0; i < len(edits); {
		if edits[i].kind == ' ' {
			i++
			continue
		}
		// A hunk: from context lines before this change to context lines
		// after the last change that is close enough to join it.
		start, end := max(0, i-context), i
		for j := i; j < len(edits); j++ {
			if edits[j].kind == ' ' {
				continue
			}
			if j > end+2*context {
				break
			}
			end = j
		}
		end = min(len(edits), end+context+1)
		if out.Len() == 0 {
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldName, newName)
		}
		hunk(&out, edits[start:end])
		i = end
	}
	return out.String()
}

// hunk writes one hunk of edits.
func hunk(out *strings.Builder, edits []edit) {
	var oldLines, newLines int
	for _, e := range edits {
		if e.kind != '+' {
			oldLines++
		}
		if e.kind != '-' {
			newLines++
		}
	}
	fmt.Fprintf(out, "@@ -%s +%s @@\n", span(edits[0].at, oldLines), span(edits[0].to, newLines))
	for _, e := range edits {
		fmt.Fprintf(out, "%c%s\n", e.kind, e.line)
	}
}

// span is a hunk's range of lines in one text: its first line (1-based) and
// count, or for no lines, the line before.
func span(at, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", at)
	}
	return fmt.Sprintf("%d,%d", at+1, n)
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diff is the shortest edit from a to b, by their longest common
// subsequence of lines, with removals before additions.
func diff(a, b []string) []edit {
	// common[i][j] is the length of the longest common subsequence of
	// a[i:] and b[j:].
	common := make([][]int, len(a)+1)
	for i := range common {
		common[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	var edits []edit
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			edits = append(edits, edit{' ', a[i], i, j})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || common[i+1][j] >= common[i][j+1]):
			edits = append(edits, edit{'-', a[i], i, j})
			i++
		default:
			edits = append(edits, edit{'+', b[j], i, j})
			j++
		}
	}
	return edits
}
