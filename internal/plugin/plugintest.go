package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/engine"
)

// A Plugin's test cases, run by `lane plugin test`, live beside its manifest:
//
//	testdata/<case>/project/        a fixture Project, mounted as the Project
//	testdata/<case>/events/*.json   sample events for its hooks
//	testdata/<case>/golden.json     what the Plugin returned, as accepted
//
// The Plugin's Recipes and Integrations that detect the fixture Project
// return its layers, its Generators their effects for it, and its hooks
// their effects for each sample event. Go tooling ignores testdata, so a
// Plugin written in Go can keep its cases beside its source.
const (
	TestDir    = "testdata"
	GoldenFile = "golden.json"
	caseEvents = "events"
	caseDir    = "project"
)

// TestCase is one test case of a Plugin, run.
type TestCase struct {
	// Name is the case's directory under TestDir.
	Name string
	// Golden is the case's golden file.
	Golden string
	// Got is what the Plugin returned, as the golden file would hold it.
	Got []byte
	// Want is the golden file's content; nil if there is none yet.
	Want []byte
	// Err is why the case couldn't be run, such as a malformed sample
	// event. A case that can't be run has no Got.
	Err error
}

// Passed reports whether the Plugin returned what the golden file holds.
func (c TestCase) Passed() bool {
	return c.Err == nil && c.Want != nil && bytes.Equal(c.Got, c.Want)
}

// WriteGolden makes what the Plugin returned the case's golden file.
func (c TestCase) WriteGolden() error {
	if c.Err != nil {
		return c.Err
	}
	return os.WriteFile(c.Golden, c.Got, 0o644)
}

// caseOutput is a golden file: what each part of the Plugin returned for
// the case. Only Recipes and Integrations that detect the fixture Project
// are run, as Laneway runs only those.
type caseOutput struct {
	Recipes      map[string]layerOutput   `json:"recipes,omitempty"`
	Integrations map[string]layerOutput   `json:"integrations,omitempty"`
	Generators   map[string]effectsOutput `json:"generators,omitempty"`
	// Events are the hooks' effects, by sample event file name without
	// .json.
	Events map[string]effectsOutput `json:"events,omitempty"`
}

// layerOutput is what a Recipe or Integration returned.
type layerOutput struct {
	// Detected is the detection rule that matched, such as "artisan
	// exists".
	Detected string `json:"detected"`
	// Error is why the call failed, or why Laneway would refuse its layer.
	Error                string          `json:"error,omitempty"`
	Layer                json.RawMessage `json:"layer,omitempty"`
	TestDatabase         bool            `json:"test_database,omitempty"`
	TestDatabaseWarnings []string        `json:"test_database_warnings,omitempty"`
	// TestEnv is the env file a Recipe says its Project's tests read.
	TestEnv *testEnv `json:"test_env,omitempty"`
	// ExportEnv is whether a Recipe says its stack doesn't read .env.
	ExportEnv bool `json:"export_env,omitempty"`
	// Warnings are those the module returned, then Laneway's about the
	// layer, such as a key it doesn't know.
	Warnings []string `json:"warnings,omitempty"`
}

// effectsOutput is what a hook or Generator returned: the effects Laneway
// would apply, and why it would refuse the others.
type effectsOutput struct {
	Error    string   `json:"error,omitempty"`
	Effects  []Effect `json:"effects,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Test runs the Plugin in the directory src, as its author has it rather
// than installed, against each of its test cases, in name order. It fails
// if the Plugin can't be read or has no test cases.
func (h *Host) Test(src string) ([]TestCase, error) {
	p, err := readSource(src)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(src, TestDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var cases []TestCase
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cases = append(cases, h.runCase(p, filepath.Join(src, TestDir, e.Name())))
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("Plugin %s has no test cases; add one as a directory under %s, holding a fixture Project in %s/ or sample events in %s/",
			p.Name, filepath.Join(src, TestDir), caseDir, caseEvents)
	}
	return cases, nil
}

// runCase runs p against the test case in dir.
func (h *Host) runCase(p Plugin, dir string) TestCase {
	c := TestCase{Name: filepath.Base(dir), Golden: filepath.Join(dir, GoldenFile)}
	if want, err := os.ReadFile(c.Golden); err == nil {
		c.Want = want
	} else if !errors.Is(err, fs.ErrNotExist) {
		c.Err = err
		return c
	}
	out, err := h.caseOutput(p, dir)
	if err != nil {
		c.Err = err
		return c
	}
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		c.Err = err
		return c
	}
	c.Got = append(got, '\n')
	return c
}

func (h *Host) caseOutput(p Plugin, dir string) (caseOutput, error) {
	var out caseOutput
	project := filepath.Join(dir, caseDir)
	hasProject := isDir(project)
	events, err := readEvents(p, filepath.Join(dir, caseEvents))
	if err != nil {
		return out, err
	}
	if !hasProject && len(events) == 0 {
		return out, fmt.Errorf("it has no fixture Project (%s/) and no sample events (%s/*.json); add one or the other", caseDir, caseEvents)
	}
	if hasProject {
		// The Plugin isn't installed, so its own Engines join Laneway's.
		engines := append(slices.Clip(p.Engines), h.Engines()...)
		for _, decl := range p.Recipes {
			matched, ok := decl.Detect.Match(project)
			if !ok {
				continue
			}
			r := pluginRecipe{host: h, plugin: p, decl: decl}
			var res recipeResponse
			err := h.call(p, r.source(), request{Call: callRecipe, Name: decl.Name}, project, &res)
			o := layerOutput{Detected: matched, Layer: res.Layer, TestDatabase: res.TestDatabase,
				TestDatabaseWarnings: res.TestDatabaseWarnings, TestEnv: res.TestEnv, ExportEnv: res.ExportEnv, Warnings: res.Warnings}
			o.check(r.source(), engines, err)
			out.Recipes = setOutput(out.Recipes, decl.Name, o)
		}
		for _, decl := range p.Integrations {
			matched, ok := decl.Detect.Match(project)
			if !ok {
				continue
			}
			in := pluginIntegration{host: h, plugin: p, decl: decl}
			var res integrationResponse
			err := h.call(p, in.source(), request{Call: callIntegration, Name: decl.Name}, project, &res)
			o := layerOutput{Detected: matched, Layer: res.Layer, Warnings: res.Warnings}
			o.check(in.source(), engines, err)
			out.Integrations = setOutput(out.Integrations, decl.Name, o)
		}
		for _, g := range p.Generators {
			what := fmt.Sprintf("the %s Generator (Plugin %s)", g.Name, p.Name)
			var res effectsResponse
			err := h.call(p, what, request{Call: callGenerator, Name: g.Name}, project, &res)
			out.Generators = setOutput(out.Generators, g.Name, checked(p, what, res.Effects, err))
		}
	}
	if len(events) > 0 {
		mount := project
		if !hasProject {
			// A hook still has a Worktree mounted: an empty one.
			empty, err := os.MkdirTemp("", "laneway-plugin-test-")
			if err != nil {
				return out, err
			}
			defer os.RemoveAll(empty)
			mount = empty
		}
		for _, ev := range events {
			effects, err := h.Hook(p, ev.Event, mount)
			out.Events = setOutput(out.Events, ev.name, checked(p, HookSource(p, ev.Name), effects, err))
		}
	}
	return out, nil
}

// check records err, or, for a layer the module returned, what Laneway
// would say about it: why it would refuse it, and its warnings.
func (o *layerOutput) check(source string, engines engine.Set, err error) {
	if err != nil {
		*o = layerOutput{Detected: o.Detected, Error: err.Error()}
		return
	}
	l, err := parseLayer(source, o.Layer, assumeServices(o.Layer, engines), nil)
	if err != nil {
		o.Error = err.Error()
		return
	}
	o.Warnings = append(o.Warnings, l.Warnings...)
}

// assumeServices is engines with an engine.Assumed for each Service the
// layer raw names whose Engine isn't among them, such as one the Catalog
// has but this machine hasn't fetched: what a Plugin returns doesn't depend
// on the Engines installed where its tests run.
func assumeServices(raw json.RawMessage, engines engine.Set) engine.Set {
	var layer struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if json.Unmarshal(raw, &layer) != nil {
		return engines // parseLayer says why
	}
	for _, name := range slices.Sorted(maps.Keys(layer.Services)) {
		if _, ok := engines.Lookup(name); !ok {
			engines = append(slices.Clip(engines), engine.Assumed(name))
		}
	}
	return engines
}

// checked is the effects a hook or Generator of p returned (or the error
// err it failed with) as Laneway would apply them: the effects p may
// return, and a warning for each one refused.
func checked(p Plugin, source string, effects []Effect, err error) effectsOutput {
	if err != nil {
		return effectsOutput{Error: err.Error()}
	}
	allowed, warnings := p.CheckEffects(source, effects)
	return effectsOutput{Effects: allowed, Warnings: warnings}
}

func setOutput[T any](m map[string]T, key string, v T) map[string]T {
	if m == nil {
		m = map[string]T{}
	}
	m[key] = v
	return m
}

// sampleEvent is a sample event, read from the file name.json.
type sampleEvent struct {
	Event
	name string
}

// readEvents reads the sample events in dir, in file name order, checking
// that each is an event p hooks.
func readEvents(p Plugin, dir string) ([]sampleEvent, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []sampleEvent
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		file := filepath.Join(caseEvents, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var ev Event
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ev); err != nil {
			return nil, fmt.Errorf("%s isn't a sample event: %w", file, err)
		}
		if !slices.Contains(p.Hooks, ev.Name) {
			return nil, fmt.Errorf("%s is a %q event, but Plugin %s doesn't hook it; add it to hooks in %s, or use an event the Plugin hooks (%s)",
				file, ev.Name, p.Name, ManifestFile, strings.Join(p.Hooks, ", "))
		}
		events = append(events, sampleEvent{Event: ev, name: name})
	}
	return events, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
