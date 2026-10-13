package scrollback

// Cached returns the number of lines the window's cache holds, counting a
// block with no lines as one. Tests use it to check the cache's bound.
func (w *Window) Cached() int { return w.cache.held }

// Entries returns the number of blocks the window's cache holds.
func (w *Window) Entries() int { return len(w.cache.entries) }
