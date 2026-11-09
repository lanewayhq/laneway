package naming

import "strings"

// MaxBucket is the longest a Bucket name may be, in S3 and RustFS.
const MaxBucket = 63

// minBucket is the shortest a Bucket name may be.
const minBucket = 3

// BucketName is the name of one of a Worktree's Buckets in an object
// storage Service. Bucket names follow their own rule, not the database
// one: lowercase letters, digits and -, 3 to 63 characters, starting and
// ending with a letter or digit, and never starting with xn--.
//
// The private Bucket (suffix "") is <project> for the main Worktree
// (worktree "") and <project>--<worktree> for others. A Bucket the Project
// declares adds its name after the Project's: <project>-<suffix> and
// <project>-<suffix>--<worktree>. The Project's and the suffix's parts are
// lowercased with every run of characters other than a letter or digit
// becoming one -, so the first -- always ends them: within a Project every
// Worktree's Buckets have names of their own, and so does each Project's
// private Bucket (see Clash). worktree is the Worktree's label, which is
// already a Domain label, unique within the Project, so it is kept as it is.
//
// A name longer than 63 characters is cut short with a hash of the
// Project's, Worktree's and suffix's names as its suffix, as database names
// are, and so is one that would be too short or start with xn--.
func BucketName(project, worktree, suffix string) string {
	name := bucketPart(project)
	if suffix != "" {
		name += "-" + bucketPart(suffix)
	}
	if worktree != "" {
		name += "--" + normalize(worktree, '-', MaxBucket)
	}
	seed := project + "\x00" + worktree + "\x00" + suffix
	switch {
	case strings.HasPrefix(name, "xn--"):
		// Punycode's prefix, which S3 refuses.
		return withHash("xn-"+name[len("xn--"):], seed, '-', MaxBucket)
	case len(name) < minBucket || len(name) > MaxBucket:
		return withHash(name, seed, '-', MaxBucket)
	}
	return name
}

// bucketPart is name as the part of a Bucket name before the first --:
// normalized with - (see normalize), each run of - then made one.
func bucketPart(name string) string {
	s := normalize(name, '-', MaxBucket)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}
