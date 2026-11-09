package plugin

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/fileblock"
)

// GeneratorDecl is a Generator a Plugin adds. Its module answers
// sdk.CallGenerator for the name with file effects, which `lane generate
// <plugin>` merges into the Worktree's files.
type GeneratorDecl struct {
	Name string `json:"name"`
}

func (m Manifest) validateGenerators() error {
	seen := map[string]bool{}
	for i, g := range m.Generators {
		if !name.MatchString(g.Name) {
			return fmt.Errorf("generators[%d]: name %q isn't a Generator name: use lowercase letters, digits and dashes, starting with a letter", i, g.Name)
		}
		if seen[g.Name] {
			return fmt.Errorf("generators[%d]: the Plugin declares Generator %s twice", i, g.Name)
		}
		seen[g.Name] = true
	}
	if len(m.Generators) > 0 && !slices.Contains(m.Capabilities.Effects, EffectFile) {
		return fmt.Errorf("generators: a Generator returns file effects, but capabilities.effects doesn't include %q; add it", EffectFile)
	}
	return nil
}

// callGenerator asks a Generator for its effects.
const callGenerator = "generator"

// generatorResponse is a module's answer to a Generator call (see
// sdk.GeneratorResult): file and warning effects.
type generatorResponse struct {
	Effects []Effect `json:"effects"`
}

// ErrUnknownPlugin is returned by Generate for a Plugin that isn't
// installed or built in.
var ErrUnknownPlugin = errors.New("unknown Plugin")

// Generate runs every Generator of the Plugin name on the Worktree at root,
// and merges the file blocks they return into its files (see fileblock). It
// writes nothing if a Generator fails, returns an effect other than file or
// warning, returns one Plugin.CheckEffects refuses, or returns a block that
// can't be merged.
func (h *Host) Generate(name, root string) (fileblock.Result, error) {
	blocks, warnings, err := h.generate(name, root)
	if err != nil {
		return fileblock.Result{}, err
	}
	res, err := fileblock.Apply(root, blocks)
	if err != nil {
		return res, err
	}
	res.Warnings = append(warnings, res.Warnings...)
	return res, nil
}

// Generated is Generate without writing anything: the files the Plugin's
// Generators name, each as it is and as Generate would leave it.
func (h *Host) Generated(name, root string) ([]fileblock.File, error) {
	blocks, _, err := h.generate(name, root)
	if err != nil {
		return nil, err
	}
	files, _, err := fileblock.Merge(root, blocks)
	return files, err
}

// generate runs every Generator of the Plugin name on the Worktree at root,
// returning the file blocks they return and their warnings.
func (h *Host) generate(name, root string) ([]fileblock.Block, []string, error) {
	p, ok := h.lookup(name)
	if !ok {
		return nil, nil, fmt.Errorf("%w %s: add it with `lane plugin add <dir>`", ErrUnknownPlugin, name)
	}
	if len(p.Generators) == 0 {
		return nil, nil, fmt.Errorf("Plugin %s has no Generators, so there is nothing to generate", name)
	}
	var blocks []fileblock.Block
	var warnings []string
	for _, g := range p.Generators {
		what := fmt.Sprintf("the %s Generator (Plugin %s)", g.Name, p.Name)
		var res generatorResponse
		if err := h.call(p, what, request{Call: callGenerator, Name: g.Name}, root, &res); err != nil {
			return nil, nil, err
		}
		for _, e := range res.Effects {
			if e.Kind != EffectFile && e.Kind != EffectWarning {
				return nil, nil, fmt.Errorf("%s returned a %q effect, but a Generator may only return %q and %q effects; nothing was written",
					what, e.Kind, EffectFile, EffectWarning)
			}
		}
		effects, refused := p.CheckEffects(what, res.Effects)
		if len(refused) > 0 {
			return nil, nil, fmt.Errorf("%s; nothing was written", strings.Join(refused, "; "))
		}
		for _, e := range effects {
			if e.Kind == EffectWarning {
				warnings = append(warnings, e.Message)
			} else {
				blocks = append(blocks, *e.File)
			}
		}
	}
	return blocks, warnings, nil
}
