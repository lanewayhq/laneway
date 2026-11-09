package engine

import "fmt"

// Buckets are the commands that make, drop and list an Engine's Buckets
// (object storage, such as RustFS's), run inside the Service's container
// with the same contract as the database commands (see Databases): no
// shell, and a command fails by exiting with a status other than 0, saying
// why on stderr. A Bucket name is lowercase letters, digits and -, 3 to 63
// characters (see naming.BucketName), so {bucket} can go into a URL or a
// shell script as it is.
type Buckets struct {
	// Exists prints something if the Bucket {bucket} exists and nothing if
	// it doesn't. A Bucket a command left unfinished doesn't exist.
	Exists []string `toml:"exists" json:"exists"`
	// Create creates {bucket}, empty and private. It runs only once Exists
	// has said {bucket} doesn't exist.
	Create []string `toml:"create" json:"create"`
	// Drop drops {bucket} with everything in it. Dropping a Bucket that
	// doesn't exist must succeed.
	Drop []string `toml:"drop" json:"drop"`
	// List prints every Bucket in the Service, one per line.
	List []string `toml:"list" json:"list"`
}

// Placeholders the Bucket commands may use.
var (
	bucketPlaceholders     = []string{"user", "password", "bucket"}
	listBucketPlaceholders = []string{"user", "password"}
)

// bucketCommand is one of the Bucket commands, for checking them all alike.
type bucketCommand struct {
	key          string
	cmd          []string
	placeholders []string
}

// commands are the Bucket commands with their keys and the placeholders
// each may use.
func (b *Buckets) commands() []bucketCommand {
	return []bucketCommand{
		{"exists", b.Exists, bucketPlaceholders},
		{"create", b.Create, bucketPlaceholders},
		{"drop", b.Drop, bucketPlaceholders},
		{"list", b.List, listBucketPlaceholders},
	}
}

// checkBuckets reports the first Bucket command Laneway can't run.
func (m *Manifest) checkBuckets() error {
	if m.Bucket == nil {
		return nil
	}
	for _, c := range m.Bucket.commands() {
		if len(c.cmd) == 0 {
			return fmt.Errorf("bucket.%s is missing: the Bucket commands are exists, create, drop and list", c.key)
		}
		if err := checkPlaceholders("bucket."+c.key, c.cmd, c.placeholders); err != nil {
			return err
		}
	}
	return nil
}

// BucketVariable is the Env Patch variable holding the Worktree's private
// Bucket in a Service of m, {<engine>_bucket}; "" if m has no Bucket
// commands.
func (m *Manifest) BucketVariable() string {
	if m.Bucket == nil {
		return ""
	}
	return m.Name + "_bucket"
}
