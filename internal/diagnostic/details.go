package diagnostic

import (
	"encoding/json"
	"slices"
)

// Block is compact stream metadata. Contents are fetched separately.
type Block struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Tier    int    `json:"tier"`
	Offset  int    `json:"offset"`
	Tokens  int    `json:"tokens"`
	Bytes   int    `json:"bytes"`
	Seq     uint64 `json:"seq,omitempty"`
	From    uint64 `json:"from,omitempty"`
	To      uint64 `json:"to,omitempty"`
	Episode uint32 `json:"episode,omitempty"`
	Hash    string `json:"hash"`
	Chain   string `json:"chain"`
	Preview string `json:"preview"`
	Fate    string `json:"fate"`
}

type Content struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Canonical string `json:"canonical"`
}

type Candidate struct {
	Episode uint32  `json:"episode"`
	Start   uint64  `json:"start"`
	End     uint64  `json:"end"`
	Cosine  float32 `json:"cosine"`
	Score   float32 `json:"score"`
	Rerank  float32 `json:"rerank"`
	Reason  string  `json:"reason"`
	Passage string  `json:"passage"`
}

type Recall struct {
	Query      string      `json:"query"`
	Threshold  float32     `json:"threshold"`
	Margin     float32     `json:"margin"`
	Warm       []uint32    `json:"warm"`
	Chosen     []uint32    `json:"chosen"`
	Candidates []Candidate `json:"candidates"`
	Status     string      `json:"status"`
}

type Episode struct {
	ID      uint32 `json:"id"`
	Start   uint64 `json:"start"`
	End     uint64 `json:"end"`
	Summary uint64 `json:"summary"`
}

// Details is an immutable, size-bounded capture kept only in diagnostic mode.
// Wire holds the actual provider request body; it never includes HTTP headers.
type Details struct {
	Blocks    []Content `json:"blocks"`
	Wire      string    `json:"wire"`
	WireBytes int       `json:"wire_bytes"`
	Recall    *Recall   `json:"recall,omitempty"`
	Episodes  []Episode `json:"episodes"`
	Truncated bool      `json:"truncated"`
}

const (
	maxDetailBytes = 2 << 20
	maxStoredBytes = 32 << 20
)

// StoreDetails saves an owned JSON copy. Large captures are explicitly marked
// truncated. The separate byte budget evicts old contents before metadata.
func (b *Bus) StoreDetails(run string, call uint64, d Details) {
	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	if len(raw) > maxDetailBytes {
		// Own the nested slices before trimming the retained representation.
		var owned Details
		if json.Unmarshal(raw, &owned) != nil {
			return
		}
		d = owned
		d.Truncated = true
		d.Wire = ""
		for i := range d.Blocks {
			if len(d.Blocks[i].Text) > 4096 {
				d.Blocks[i].Text = d.Blocks[i].Text[:4096]
			}
			if len(d.Blocks[i].Canonical) > 4096 {
				d.Blocks[i].Canonical = d.Blocks[i].Canonical[:4096]
			}
		}
		raw, err = json.Marshal(d)
		if err != nil || len(raw) > maxDetailBytes {
			return
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.run != run || !slices.ContainsFunc(b.history, func(s Snapshot) bool { return s.Call == call }) {
		return
	}
	if b.details == nil {
		b.details = make(map[uint64][]byte)
	}
	b.detailBytes -= len(b.details[call])
	b.details[call] = raw
	b.detailBytes += len(raw)
	for _, s := range b.history {
		if b.detailBytes <= maxStoredBytes {
			break
		}
		b.detailBytes -= len(b.details[s.Call])
		delete(b.details, s.Call)
	}
}

func (b *Bus) Details(run string, call uint64) (Snapshot, Details, bool) {
	b.mu.Lock()
	var snapshot Snapshot
	var raw []byte
	if b.run == run {
		for _, s := range b.history {
			if s.Call == call {
				snapshot = s.clone()
				raw = b.details[call]
				break
			}
		}
	}
	b.mu.Unlock()
	// Stored byte slices are immutable. Decode after releasing the publishing lock.
	var d Details
	if raw == nil || json.Unmarshal(raw, &d) != nil {
		return snapshot, d, false
	}
	return snapshot, d, true
}

// Update publishes progress without completing the call.
func (b *Bus) Update(s Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || s.Run != b.run {
		return
	}
	for i := range b.history {
		if b.history[i].Call == s.Call && !b.history[i].Done {
			b.history[i] = s.clone()
			b.broadcast(s)
			return
		}
	}
}
