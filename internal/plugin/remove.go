package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Remove uninstalls the installed Plugin called name and returns it. It
// doesn't check whether a Project uses it: the daemon, which knows the
// Linked Projects, does that first.
func (h *Host) Remove(name string) (Plugin, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.plugins[name]
	if !ok {
		return Plugin{}, fmt.Errorf("no Plugin %s is installed", name)
	}
	// Move it aside first, so a failure part way through never leaves a
	// half-removed Plugin where Open would load it.
	tmp, err := os.MkdirTemp(h.opts.Dir, ".remove-"+name+"-")
	if err != nil {
		return Plugin{}, err
	}
	gone := filepath.Join(tmp, name)
	if err := os.Rename(p.Dir, gone); err != nil {
		os.Remove(tmp)
		return Plugin{}, err
	}
	delete(h.plugins, name)
	h.runtime.forget(name)
	if err := os.RemoveAll(tmp); err != nil {
		h.opts.Log.Warn("deleting a removed Plugin's files", "plugin", name, "dir", tmp, "err", err)
	}
	return p, nil
}

// IsBuiltIn reports whether name is a Plugin built into Laneway, which isn't
// installed and so can't be removed.
func (h *Host) IsBuiltIn(name string) bool {
	return slices.ContainsFunc(builtin(), func(p Plugin) bool { return p.Name == name })
}
