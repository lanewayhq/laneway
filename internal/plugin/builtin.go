package plugin

// lookup returns the installed Plugin name.
func (h *Host) lookup(name string) (Plugin, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.plugins[name]
	return p, ok
}

// Has reports whether a Plugin called name is installed.
func (h *Host) Has(name string) bool {
	_, ok := h.lookup(name)
	return ok
}
