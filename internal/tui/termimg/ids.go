package termimg

// IDs hands out kitty image ids. There are 255 of them, 1 to 255, because an
// id reaches the terminal as a colour of the 256-colour palette. A key keeps
// its id for as long as it is asked for; when all are taken, the id that was
// asked for longest ago goes to the new key.
//
// An IDs is not safe for concurrent use.
type IDs struct {
	next int               // where the search for a free id starts: 0 to 254
	held [maxID + 1]bool   // held[id] says id belongs to a key
	keys [maxID + 1]string // keys[id] is that key
	used [maxID + 1]uint64 // used[id] is when it was last asked for
	ids  map[string]int
	tick uint64
}

// NewIDs returns a pool whose first id follows from base: base 0 starts at 1,
// base 1 at 2, and so on round the 255. Two clients drawing in one terminal
// share its ids, so a caller takes base from the clock to make it unlikely
// that both start at the same one.
func NewIDs(base int) *IDs {
	return &IDs{next: (base%maxID + maxID) % maxID, ids: make(map[string]int)}
}

// Get returns key's id, and marks it as the most recently used.
//
// fresh is true when the id was given to key by this call: the terminal
// holds no picture for it yet, or holds another key's. evicted is then the
// key the id was taken from, or "" when the id was free.
func (p *IDs) Get(key string) (id int, evicted string, fresh bool) {
	p.tick++
	return p.get(key, p.tick)
}

// get is Get on the caller's clock: at is when key was last wanted, and the
// id taken when none is free is the one whose key was wanted longest ago by
// that clock. A Store passes when the image was last shown, so that a job for
// an image scrolled past long ago does not count as a use.
func (p *IDs) get(key string, at uint64) (id int, evicted string, fresh bool) {
	if id, ok := p.ids[key]; ok {
		p.used[id] = max(p.used[id], at)
		return id, "", false
	}
	if id = p.oldest(); id != 0 {
		evicted = p.keys[id]
		delete(p.ids, evicted)
	} else {
		for p.held[p.next+1] {
			p.next = (p.next + 1) % maxID
		}
		id = p.next + 1
		p.next = (p.next + 1) % maxID
	}
	p.held[id], p.keys[id], p.used[id] = true, key, at
	p.ids[key] = id
	return id, evicted, true
}

// oldest returns the id a new key would take from another: the one used
// longest ago. It returns 0 while an id is free.
func (p *IDs) oldest() int {
	if len(p.ids) < maxID {
		return 0
	}
	id := 1
	for i := 2; i <= maxID; i++ {
		if p.used[i] < p.used[id] {
			id = i
		}
	}
	return id
}

// Live returns every id that belongs to a key, in ascending order.
func (p *IDs) Live() []int {
	out := make([]int, 0, len(p.ids))
	for id := 1; id <= maxID; id++ {
		if p.held[id] {
			out = append(out, id)
		}
	}
	return out
}

// peek returns key's id without marking it used, or 0.
func (p *IDs) peek(key string) int { return p.ids[key] }

// release frees key's id and returns it, or 0 when key has none.
func (p *IDs) release(key string) int {
	id, ok := p.ids[key]
	if !ok {
		return 0
	}
	delete(p.ids, key)
	p.held[id], p.keys[id], p.used[id] = false, "", 0
	return id
}

// reset frees every id. The search for the next one carries on from where
// it was, so the ids just freed are the last to be used again.
func (p *IDs) reset() {
	p.held, p.keys, p.used = [maxID + 1]bool{}, [maxID + 1]string{}, [maxID + 1]uint64{}
	clear(p.ids)
}
