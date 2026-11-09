package sdk

// CallGenerator asks a Generator for the file blocks it merges into the
// Worktree, when the developer runs `lane generate <plugin>`.
const CallGenerator = "generator"

// Generator answers a CallGenerator: it reads the Worktree (Project.Dir is
// the Worktree's directory, read-only) and returns what Laneway should merge
// into its files.
type Generator func(Project) (GeneratorResult, error)

// GeneratorResult is what a Generator returns: file effects, and warning
// effects for the developer. Laneway merges every file block at once, or
// none if one can't be merged.
type GeneratorResult struct {
	Effects []Effect `json:"effects"`
}

// File is a file effect merging b. It needs the "file" effect.
func File(b FileBlock) Effect { return Effect{Kind: EffectFile, File: &b} }

// FileBlock is the part of one file Laneway owns: exactly one of Markdown,
// RemoveMarkdown and JSON is set. Laneway never overwrites the rest of the
// file, and merging the same block again changes nothing.
type FileBlock struct {
	// Path is the file, relative to the Worktree, with forward slashes.
	Path string `json:"path"`
	// Markdown is the body of Laneway's marked block in the file, which is
	// created if missing. A file has one Laneway block.
	Markdown *string `json:"markdown,omitempty"`
	// RemoveMarkdown takes Laneway's marked block out of the file.
	RemoveMarkdown bool `json:"remove_markdown,omitempty"`
	// JSON is what Laneway owns in the JSON object the file holds.
	JSON *JSONBlock `json:"json,omitempty"`
	// Warning is shown to the developer when this block changes the file.
	Warning string `json:"warning,omitempty"`
}

// Markdown is a block that puts body between Laneway's markers in path.
func Markdown(path, body string) FileBlock { return FileBlock{Path: path, Markdown: &body} }

// JSONBlock is what Laneway owns in a JSON file.
type JSONBlock struct {
	// New is the object a file that doesn't exist yet starts as.
	New any `json:"new,omitempty"`
	// Entries are what Laneway sets, in order.
	Entries []JSONEntry `json:"entries,omitempty"`
}

// JSONEntry is one value Laneway owns in a JSON file. At is a path of object
// keys. Without Match, Laneway owns the key At and sets it to Value. With
// Match, At holds an array, and Laneway owns the element whose keys match
// Match's (an array in Match matches an array containing a match for each of
// its elements): Value's keys are set in it, or Value is appended if no
// element matches. Where Match has an array of objects under a key, Value's
// array under that key is merged into the element's the same way.
type JSONEntry struct {
	At    []string `json:"at"`
	Match any      `json:"match,omitempty"`
	Value any      `json:"value"`
}
