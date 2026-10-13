package scrollback

// entry is one drawn block.
type entry struct {
	key   Key
	rev   uint64
	lines []string
	// gen is the generation the entry was last in view in.
	gen uint64
	// newer and older link the entries by when each was last used.
	newer, older *entry
}

// cache keeps drawn blocks by key. It never drops a block in view, and drops
// the others, least recently used first, once it holds more lines than its
// limit.
type cache struct {
	entries map[Key]*entry
	// newest and oldest are the ends of the list of entries by last use.
	newest, oldest *entry
	// held is the number of lines in the cache.
	held  int
	limit int
	// gen numbers the views. An entry whose gen is the current one is in
	// view and is kept.
	gen uint64
	// loose is set while a scroll passes over blocks: what is fetched then
	// is not in view, and is kept only while there is room.
	loose bool
	stats Stats
}

// begin starts a new view: no entry counts as in view until it is fetched
// again.
func (c *cache) begin() { c.gen++ }

// lines returns block i's lines, drawing the block when the cache has no
// copy at its current Rev.
func (c *cache) lines(src Source, i, width int) []string {
	k, rev := src.Key(i), src.Rev(i)
	e := c.entries[k]
	switch {
	case e != nil && e.rev == rev:
		c.stats.Hits++
		c.unlink(e)
	default:
		if e == nil {
			e = &entry{key: k}
			c.entries[k] = e
		} else {
			c.held -= cost(e.lines)
			c.unlink(e)
		}
		e.rev, e.lines = rev, src.Lines(i, width)
		c.stats.Rendered++
		c.held += cost(e.lines)
	}
	c.link(e)
	if c.loose {
		c.trim(e)
	} else {
		e.gen = c.gen
	}
	return e.lines
}

// peek returns how many lines block i has, when the cache holds it at its
// current Rev. It draws nothing and counts nothing.
func (c *cache) peek(src Source, i int) (int, bool) {
	e := c.entries[src.Key(i)]
	if e == nil || e.rev != src.Rev(i) {
		return 0, false
	}
	return len(e.lines), true
}

// trim drops entries, least recently used first, until the cache is within
// its limit. It never drops an entry in view, nor keep.
func (c *cache) trim(keep *entry) {
	for e := c.oldest; e != nil && c.held > c.limit; {
		next := e.newer
		if e != keep && e.gen != c.gen {
			c.drop(e)
		}
		e = next
	}
}

func (c *cache) drop(e *entry) {
	c.unlink(e)
	delete(c.entries, e.key)
	c.held -= cost(e.lines)
}

func (c *cache) clear() {
	clear(c.entries)
	c.newest, c.oldest, c.held = nil, nil, 0
}

// link puts an entry at the newest end of the list.
func (c *cache) link(e *entry) {
	e.newer, e.older = nil, c.newest
	if c.newest != nil {
		c.newest.newer = e
	} else {
		c.oldest = e
	}
	c.newest = e
}

// unlink takes an entry out of the list.
func (c *cache) unlink(e *entry) {
	if e.newer != nil {
		e.newer.older = e.older
	} else if c.newest == e {
		c.newest = e.older
	}
	if e.older != nil {
		e.older.newer = e.newer
	} else if c.oldest == e {
		c.oldest = e.newer
	}
	e.newer, e.older = nil, nil
}

// cost is what an entry counts for against the limit. A block with no lines
// still counts, so that the number of entries is bounded too.
func cost(lines []string) int { return max(len(lines), 1) }
