package termimg

import (
	"context"
	"errors"
	"image"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
)

const (
	// boxCols and boxRows are the largest box an image is drawn in, in
	// cells. A master is scaled to fill it and no further.
	boxCols, boxRows = 44, 12
	// masterSide is the longest side of a master, in pixels, whatever the
	// cell size.
	masterSide = 1024
	// defaultCacheBytes is how many bytes of masters a store keeps.
	defaultCacheBytes = 16 << 20
	// defaultCellW and defaultCellH are a cell's size until the caps say.
	defaultCellW, defaultCellH = 8, 16
	// decodeTime is how long a job gives one file's decoding. An honest
	// picture of the largest size allowed takes about a second; a file made
	// to be slow can take minutes, and nothing else loads while it does.
	decodeTime = 10 * time.Second
	// rowsKept is how many images keep their encoded rows. It is far more
	// than fit on a screen, and bounds what a long scroll through history
	// leaves behind.
	rowsKept = 128
)

// Ref names one image of one reply.
type Ref struct {
	// Seq is the reply's sequence number.
	Seq uint64
	// N counts the images in the reply from 1.
	N int
	// Path is the file, relative to the contact's working directory, as a
	// Fetch takes it.
	Path string
	// Alt is the image's alternative text. The store keeps it and draws
	// nothing from it.
	Alt string
	// TS is when the reply was written, in unix milliseconds, or zero.
	TS int64
}

// Shown is what to draw for an image right now. The strings carry no indent
// and, but for the colours inside Rows, no style: the caller adds both.
type Shown struct {
	// Rows is the picture, one string per row of cells. It is nil at the
	// text level, while the image is loading, and when it failed. The slice
	// is the store's own: do not change it.
	Rows []string
	// Caption goes under Rows: the name, the sizes and how to save the file.
	Caption string
	// Line is the text line, when Rows is nil.
	Line string
	// Hint goes under Line once the file is known to be an image:
	// "/image save 31 ./latency.png".
	Hint string
}

// FileData is what a Fetch returns.
type FileData struct {
	// Data is the file's bytes. It is empty when NotModified is set.
	Data []byte
	// ETag identifies this version of the file, as the server gave it.
	ETag string
	// Modified is when the file last changed, or zero.
	Modified time.Time
	// NotModified means the file still has the ETag that was asked about.
	NotModified bool
}

// Fetch reads one file from the contact's working directory. A non-empty
// etag makes the request conditional: when the file still has that ETag the
// answer is NotModified with no data.
//
// The error says which line the image gets: one that wraps ErrNotFound when
// the file does not exist, ErrTooLarge when the server would not send it
// whole, and anything else when it could not be read this time.
type Fetch func(ctx context.Context, path, etag string) (FileData, error)

// Job is the slow part of showing one image: fetching the file, decoding it,
// scaling it and encoding it for the terminal. Run it off the update loop,
// on any goroutine, and hand what it returns to Finish. It touches nothing of
// the store, and it always returns: a failed fetch, a cancelled context and a
// file that cannot be decoded are all in the Done.
//
// Cancelling ctx stops the fetch, if the Fetch honours it, and stops the
// decoding within a few hundred bytes of file. A file that takes longer than
// ten seconds to decode is given up on, and is "could not be read".
type Job func(ctx context.Context, fetch Fetch) Done

// Done is the result of a Job, for Finish. The zero value is no result.
type Done struct {
	owner  *Store // the store whose job this was
	serial uint64
	key    entryKey
	epoch  uint64
	gen    uint64
	fail   failure
	pic    *picture
	loaded bool // pic is new and belongs in the cache
	sent   *placement
}

// StoreOptions configure a Store. A zero field takes its default.
type StoreOptions struct {
	// Limits bound the files a job decodes. Default: DefaultLimits.
	Limits Limits
	// CacheBytes is how many bytes of scaled-down pictures are kept.
	// Default: 16 MiB.
	CacheBytes int
	// IDBase chooses the first kitty image id; see NewIDs.
	IDBase int
	// MaxCols and MaxRows bound image grids and their retained masters.
	// Zero keeps the thread defaults (44 by 12); stages can request more.
	MaxCols, MaxRows int
}

// Stats counts the work a Store has done, for tests that pin how little
// that is.
type Stats struct {
	// Jobs is the number of jobs Next handed out.
	Jobs int
	// Encodes is the number of times a picture was encoded for the screen:
	// as rows of half blocks, or as a kitty transmit.
	Encodes int
}

// entryKey identifies an image: the reply and the image's number in it.
type entryKey struct {
	seq uint64
	n   int
}

// state is how far an image has come.
type state uint8

const (
	// stateWanted means the file has to be fetched.
	stateWanted state = iota + 1
	// stateReady means the file was read and is an image.
	stateReady
	// stateFailed means the last attempt failed; fail says how.
	stateFailed
)

// picture is a file's picture, scaled down, with what is known of the file.
// It is never changed once made, so a job may read one while the store
// holds it.
type picture struct {
	img      *image.RGBA
	info     Info
	bytes    int64
	etag     string
	modified time.Time
}

// placement is a picture the terminal holds: its id and the grid it is
// placed on. raw is the transmit that puts it there.
type placement struct {
	id, cols, rows int
	raw            string
}

// entry is one image the store has been asked to show.
type entry struct {
	ref    Ref
	key    entryKey
	number string // "31" or "31.2": what /image calls it, and its key for an id
	name   string // the file's base name, as the reply wrote it
	epoch  uint64 // changes when the reply's image becomes another file

	state state
	fail  failure

	// What the last fetch found. It outlives the master's stay in the
	// cache, so the text line stays whole while the file is fetched again.
	info     Info
	bytes    int64
	etag     string
	modified time.Time

	shown            uint64 // when Show was last called for it
	maxCols, maxRows int    // the box it was last shown in

	// rows is the picture as last encoded, at blocks for a box of
	// rowsCols by rowsRows cells, at pixels for the placement.
	rows               []string
	rowsCols, rowsRows int
	// placed means the terminal holds the picture as place describes.
	placed bool
	place  placement
	// transmit means the picture is ready and has to be sent to the
	// terminal again: for a new box, or under a new id.
	transmit bool
}

// Store shows the images of one contact's replies. See the package comment
// for the steps; this is how a caller drives them:
//
//   - Show, while drawing, for each image in view. It answers at once.
//   - Next, after each update. When it returns a job, run it off the loop.
//   - Finish, with what the job returned. Draw the reply again, and write
//     the bytes it returns to the terminal outside the view.
//   - SetCaps when the terminal's capabilities change, DeleteAll on leaving.
//
// A store belongs to one contact: sequence numbers and paths mean nothing
// across two. It is not safe for concurrent use; only a Job may run on
// another goroutine.
type Store struct {
	limits           Limits
	cache            *cache
	ids              *IDs
	maxCols, maxRows int

	level        termcaps.Images
	ascii        bool
	tmux         bool
	cellW, cellH int
	gen          uint64 // changes when every encoding becomes stale

	entries  map[entryKey]*entry
	byNumber map[string]*entry
	queue    map[*entry]struct{} // entries that need a job
	withRows map[*entry]struct{} // entries that hold encoded rows
	tick     uint64
	epoch    uint64

	serial uint64
	out    uint64 // the serial of the job that is out, or zero
	// sent[id] says the terminal was sent a picture under id and has not
	// been told to drop it. drop lists the ids it is to be told to drop
	// with the next bytes that go to it.
	sent [maxID + 1]bool
	drop []int
	// stale lists the replies whose drawn cells name an id that has gone to
	// another image, until Stale hands them out.
	stale []uint64
	stats Stats
}

// NewStore returns an empty store that draws text lines until SetCaps says
// the terminal can do more.
func NewStore(o StoreOptions) *Store {
	if o.CacheBytes <= 0 {
		o.CacheBytes = defaultCacheBytes
	}
	if o.MaxCols <= 0 {
		o.MaxCols = boxCols
	}
	if o.MaxRows <= 0 {
		o.MaxRows = boxRows
	}
	return &Store{
		limits:  o.Limits.filled(),
		cache:   newCache(o.CacheBytes),
		ids:     NewIDs(o.IDBase),
		maxCols: min(o.MaxCols, 160), maxRows: min(o.MaxRows, 60),
		level:    termcaps.ImagesText,
		cellW:    defaultCellW,
		cellH:    defaultCellH,
		entries:  make(map[entryKey]*entry),
		byNumber: make(map[string]*entry),
		queue:    make(map[*entry]struct{}),
		withRows: make(map[*entry]struct{}),
	}
}

// SetCaps tells the store what the terminal can do. When that changes how an
// image is drawn (the image level, the cell size, tmux passthrough or the
// glyph set) every encoding is dropped, and SetCaps returns the sequence
// numbers of the replies to draw again, in ascending order, and the bytes
// that make the terminal forget the pictures it was sent. Nothing is fetched
// again: pictures are encoded anew from the masters, as they are next shown.
// The one exception is a cell that grew, at the pixel level: a master made
// for the smaller cells holds too few pixels for the new grid, so the job
// that sends such a picture fetches its file once more.
//
// Otherwise it returns nil and "".
func (s *Store) SetCaps(c termcaps.Caps) (changed []uint64, raw string) {
	level := c.Images
	switch level {
	case termcaps.ImagesText, termcaps.ImagesBlocks, termcaps.ImagesPixels:
	default:
		level = termcaps.ImagesText
	}
	cellW, cellH := c.CellW, c.CellH
	if cellW <= 0 || cellH <= 0 {
		cellW, cellH = defaultCellW, defaultCellH
	}
	ascii := c.Glyphs == termcaps.GlyphsASCII
	glyphs := ascii != s.ascii
	if !glyphs && level == s.level && cellW == s.cellW && cellH == s.cellH && c.Tmux == s.tmux {
		return nil, ""
	}
	// The pictures were sent the old way, so they are deleted the old way.
	raw = s.forget()
	s.level, s.cellW, s.cellH, s.tmux, s.ascii = level, cellW, cellH, c.Tmux, ascii
	for _, e := range s.entries {
		// A loading or failed image is a line of text, which only the
		// glyph set changes.
		if e.state == stateReady || glyphs {
			changed = append(changed, e.ref.Seq)
		}
	}
	slices.Sort(changed)
	return slices.Compact(changed), raw
}

// Show returns what to draw for an image in a box of at most maxCols by
// maxRows cells, and notes that the image is wanted. It never waits: until
// the picture is ready the answer is a text line.
//
// The box is cut to the configured maximum, and the picture keeps its shape inside
// it. Asking again with the same box costs nothing; a new box encodes the
// picture again from its master. At the pixel level the new grid has to be
// sent to the terminal first, so the answer is the text line until the job
// that Next hands out for it has been through Finish.
//
// Show is also the store's clock. There are 255 kitty ids, and when all are
// taken a picture's id comes from the image shown longest ago. An image
// drawn from the caller's own cache of rows, without a call to Show, does
// not count as shown; see Stale.
func (s *Store) Show(ref Ref, maxCols, maxRows int) Shown {
	e := s.entryFor(ref)
	s.tick++
	e.shown = s.tick
	e.maxCols, e.maxRows = min(max(maxCols, 1), s.maxCols), min(max(maxRows, 1), s.maxRows)

	switch e.state {
	case stateWanted:
		return s.text(e)
	case stateFailed:
		return Shown{Line: failLine(e.name, e.fail, s.ascii)}
	case stateReady:
	}
	cols, rows := s.fit(e)
	switch s.level {
	case termcaps.ImagesText:
		return s.text(e)
	case termcaps.ImagesBlocks:
		if e.rows == nil || e.rowsCols != cols || e.rowsRows != rows {
			pic := s.cache.get(e.ref.Path, e.etag)
			if pic == nil {
				s.want(e)
				return s.text(e)
			}
			s.encodeBlocks(e, pic)
		}
		return Shown{Rows: e.rows, Caption: s.caption(e)}
	case termcaps.ImagesPixels:
		id := s.id(e)
		if e.placed && e.place.id == id && e.place.cols == cols && e.place.rows == rows {
			e.transmit = false
			if e.rows == nil || e.rowsCols != cols || e.rowsRows != rows {
				s.setRows(e, KittyPlaceholders(id, cols, rows), cols, rows)
			}
			return Shown{Rows: e.rows, Caption: s.caption(e)}
		}
		// The terminal holds another grid, or nothing. What it holds stays
		// noted in place: a box that comes back needs no new transmit.
		s.dropRows(e)
		if s.cache.get(e.ref.Path, e.etag) == nil {
			s.want(e)
		} else {
			e.transmit = true
			s.queue[e] = struct{}{}
		}
		return s.text(e)
	}
	return s.text(e)
}

// Measure returns an image's fitted cell dimensions without marking it shown,
// creating an entry, scheduling work, or changing its terminal placement.
// Unknown files return false; callers can measure their loading label instead.
func (s *Store) Measure(ref Ref, maxCols, maxRows int) (cols, rows int, known bool) {
	e := s.entries[entryKey{seq: ref.Seq, n: ref.N}]
	if e == nil || e.ref.Path != ref.Path || e.info.W <= 0 || e.info.H <= 0 || s.level == termcaps.ImagesText {
		return 0, 0, false
	}
	copy := *e
	copy.maxCols, copy.maxRows = min(max(maxCols, 1), s.maxCols), min(max(maxRows, 1), s.maxRows)
	cols, rows = s.fit(&copy)
	return cols, rows, true
}

// Caption describes a known image without marking it shown or scheduling it.
func (s *Store) Caption(ref Ref) string {
	e := s.entries[entryKey{seq: ref.Seq, n: ref.N}]
	if e == nil || e.ref.Path != ref.Path || e.info.W <= 0 {
		return ""
	}
	return s.caption(e)
}

// Next returns the next job to run, if there is work and no job is out. At
// most one job is out at a time: after a job is handed out Next returns false
// until Finish has had its result. The image shown most recently goes first.
//
// Every image that was shown gets its file read, in time. At the pixel level
// only the 255 shown last are sent to the terminal; one shown before them is
// sent when it is shown again.
//
// When there is nothing to do Next costs nothing, so it can be called after
// every update.
func (s *Store) Next() (Job, bool) {
	if s.out != 0 || len(s.queue) == 0 {
		return nil, false
	}
	var e *entry
	for c := range s.queue {
		if !s.needsJob(c) {
			delete(s.queue, c)
			continue
		}
		if e == nil || c.shown > e.shown {
			e = c
		}
	}
	if e == nil {
		return nil, false
	}
	delete(s.queue, e)
	e.transmit = false
	s.serial++
	s.out = s.serial
	s.stats.Jobs++

	p := &plan{
		owner: s, serial: s.serial, key: e.key, epoch: e.epoch, gen: s.gen,
		path: e.ref.Path, limits: s.limits, cellW: s.cellW, cellH: s.cellH,
		limitCols: s.maxCols, limitRows: s.maxRows,
	}
	// An image shown before every image that holds an id gets none: its
	// file is read, and its picture is sent when it is shown again.
	if s.level == termcaps.ImagesPixels && s.canPlace(e) {
		p.id, p.maxCols, p.maxRows, p.tmux = s.id(e), e.maxCols, e.maxRows, s.tmux
	}
	if e.state == stateReady {
		// Only the encoding is missing, if the master is still here.
		p.pic = s.cache.get(e.ref.Path, e.etag)
	}
	if p.pic == nil {
		p.known = s.cache.newest(e.ref.Path)
	}
	return p.run, true
}

// Finish takes the result of the job that is out. It returns the sequence
// number of the reply to draw again, and the bytes to write to the terminal
// outside the view, or "": the kitty transmit of the picture, when there is
// one, after the deletes of any pictures whose ids were taken for others. A
// result that is not the outstanding job's is ignored (the zero Done, one
// handed in twice, one from another store), and so is the result for an image
// the reply no longer holds: all return zero and "".
func (s *Store) Finish(d Done) (seq uint64, raw string) {
	if d.owner != s || d.serial == 0 || d.serial != s.out {
		return 0, ""
	}
	s.out = 0
	if d.sent != nil {
		s.stats.Encodes++
	}
	e := s.entries[d.key]
	if e == nil || e.epoch != d.epoch {
		return 0, ""
	}
	if d.fail != 0 || d.pic == nil {
		e.state, e.fail = stateFailed, d.fail
		if e.fail == 0 {
			e.fail = failUnreadable
		}
		e.placed, e.transmit = false, false
		s.dropRows(e)
		delete(s.queue, e)
		// A failed image shows no picture, so it holds no id.
		if id := s.ids.release(e.number); id != 0 && s.sent[id] {
			s.drop = append(s.drop, id)
		}
		return e.ref.Seq, s.drops()
	}
	pic := d.pic
	if d.loaded {
		s.cache.put(e.ref.Path, pic)
	}
	if e.state != stateReady || e.etag != pic.etag {
		// Another version of the file: what was drawn is out of date.
		e.placed = false
		s.dropRows(e)
	}
	e.state, e.fail = stateReady, 0
	e.info, e.bytes, e.etag, e.modified = pic.info, pic.bytes, pic.etag, pic.modified

	cols, rows := s.fit(e)
	switch s.level {
	case termcaps.ImagesText:
	case termcaps.ImagesBlocks:
		if e.rows == nil || e.rowsCols != cols || e.rowsRows != rows {
			s.encodeBlocks(e, pic)
		}
	case termcaps.ImagesPixels:
		// The transmit counts only if it is still what the screen needs:
		// same caps, same id, same grid. If not, Show asks for another.
		sent := d.sent
		if sent != nil && d.gen == s.gen && s.ids.peek(e.number) == sent.id && sent.cols == cols && sent.rows == rows {
			e.placed, e.place = true, placement{id: sent.id, cols: cols, rows: rows}
			e.transmit = false
			s.setRows(e, KittyPlaceholders(sent.id, cols, rows), cols, rows)
			raw = s.drops() + sent.raw
			s.sent[sent.id] = true
			return e.ref.Seq, raw
		}
	}
	return e.ref.Seq, s.drops()
}

// Retry makes every failed image wanted again, so that Next hands out a job
// for each. A reply is drawn again when its job is through Finish.
func (s *Store) Retry() {
	for _, e := range s.entries {
		if e.state == stateFailed {
			s.want(e)
		}
	}
}

// Lookup returns the image that Show was asked about under a reply's
// sequence number and the image's number in it, counted from 1.
func (s *Store) Lookup(seq uint64, n int) (Ref, bool) {
	e, ok := s.entries[entryKey{seq: seq, n: max(n, 1)}]
	if !ok {
		return Ref{}, false
	}
	return e.ref, true
}

// DeleteAll returns the bytes that make the terminal forget every picture
// the store sent it, one kitty delete for each id in use, or "" when there is
// none. Write them when leaving the contact and after the program ends. The
// store then takes it that the terminal has nothing: an image shown again is
// sent again.
func (s *Store) DeleteAll() string {
	return s.forget()
}

// Stats returns the counts of work done so far.
func (s *Store) Stats() Stats { return s.stats }

// Stale returns the sequence numbers of the replies that have to be drawn
// again although no job finished for them, in ascending order, and forgets
// them. Each showed a picture whose kitty id has since gone to another image:
// rows drawn for it earlier, and kept by the caller, now name the other
// picture. Drawn again, the image is its text line until it has been sent
// under a new id.
//
// It is nil until more than 255 images have been shown at the pixel level.
// Call it after each Finish and treat the replies as Finish's own.
func (s *Store) Stale() []uint64 {
	if len(s.stale) == 0 {
		return nil
	}
	out := s.stale
	s.stale = nil
	slices.Sort(out)
	return slices.Compact(out)
}

// drops returns the deletes that were waiting to go to the terminal.
func (s *Store) drops() string {
	if len(s.drop) == 0 {
		return ""
	}
	var raw strings.Builder
	for _, id := range s.drop {
		if s.sent[id] {
			raw.WriteString(KittyDelete(id, s.tmux))
			s.sent[id] = false
		}
	}
	s.drop = s.drop[:0]
	return raw.String()
}

// forget drops every encoding and returns the deletes for every picture the
// terminal was sent, in the order of their ids. A transmit still being made
// by a job is dropped with them: Finish will not pass it on.
func (s *Store) forget() string {
	var raw strings.Builder
	for id, sent := range s.sent {
		if sent {
			raw.WriteString(KittyDelete(id, s.tmux))
		}
	}
	s.sent, s.drop = [maxID + 1]bool{}, s.drop[:0]
	s.ids.reset()
	s.gen++
	for _, e := range s.entries {
		e.placed, e.transmit = false, false
		e.rows = nil
		if e.state == stateReady {
			delete(s.queue, e)
		}
	}
	clear(s.withRows)
	return raw.String()
}

// entryFor returns the entry for a reference, making it when it is new. A
// reference whose reply now names another file starts again from nothing.
func (s *Store) entryFor(ref Ref) *entry {
	key := entryKey{seq: ref.Seq, n: max(ref.N, 1)}
	e := s.entries[key]
	if e != nil && e.ref.Path == ref.Path {
		e.ref = ref
		return e
	}
	if e == nil {
		number := strconv.FormatUint(key.seq, 10)
		if key.n > 1 {
			number += "." + strconv.Itoa(key.n)
		}
		e = &entry{key: key, number: number}
		s.entries[key] = e
		s.byNumber[number] = e
	}
	s.dropRows(e)
	s.epoch++
	*e = entry{ref: ref, key: key, number: e.number, name: path.Base(ref.Path), epoch: s.epoch}
	s.want(e)
	return e
}

// needsJob reports whether a queued image still has work for a job: its file
// to fetch, or its picture to send to the terminal.
func (s *Store) needsJob(e *entry) bool {
	switch e.state {
	case stateWanted:
		return true
	case stateReady:
		return e.transmit && s.level == termcaps.ImagesPixels && s.canPlace(e)
	case stateFailed:
	}
	return false
}

// canPlace reports whether an image may hold a kitty id: it has one, or one
// is free, or the image that would lose its id was shown before this one. An
// image scrolled past long ago therefore never takes the id of one shown
// since, however late its job runs.
func (s *Store) canPlace(e *entry) bool {
	if s.ids.peek(e.number) != 0 {
		return true
	}
	oldest := s.ids.oldest()
	return oldest == 0 || s.ids.used[oldest] < e.shown
}

// want marks an image as needing its file fetched.
func (s *Store) want(e *entry) {
	e.state, e.fail, e.transmit = stateWanted, 0, false
	s.queue[e] = struct{}{}
}

// id returns the image's kitty id, giving it one if it has none. When that
// takes the id from another image, that image is no longer placed, its reply
// is listed for Stale, and the terminal is told to drop its picture with the
// next bytes sent. The ids' clock is when each image was last shown.
func (s *Store) id(e *entry) int {
	id, evicted, _ := s.ids.get(e.number, e.shown)
	if evicted != "" {
		if old := s.byNumber[evicted]; old != nil {
			if old.placed {
				s.stale = append(s.stale, old.ref.Seq)
			}
			old.placed = false
			s.dropRows(old)
		}
		if s.sent[id] {
			s.drop = append(s.drop, id)
		}
	}
	return id
}

// fit returns the grid the image takes in the box it was last shown in.
//
// As pixels a picture is never drawn larger than it is. As half blocks one
// pixel of it can be no smaller than half a cell, so there the picture
// counts as that many times its size: a small one takes the cells that show
// each of its pixels once, and no more. Without this a 16x16 icon would be
// two cells of four colours.
func (s *Store) fit(e *entry) (cols, rows int) {
	w, h := e.info.W, e.info.H
	if s.level == termcaps.ImagesBlocks {
		// The pixels in the smaller side of a half cell.
		k := max(min(s.cellW, s.cellH/2), 1)
		w, h = times(w, k), times(h, k)
	}
	return Fit(w, h, s.cellW, s.cellH, max(e.maxCols, 1), max(e.maxRows, 1))
}

// times returns n*k, held to the largest size Fit reads, whatever is passed.
func times(n, k int) int {
	const most = 1 << 30
	return int(min(int64(min(n, most))*int64(min(k, most)), most))
}

// encodeBlocks draws the image as half blocks for the box it was last shown
// in.
func (s *Store) encodeBlocks(e *entry, pic *picture) {
	cols, rows := s.fit(e)
	s.stats.Encodes++
	s.setRows(e, HalfBlocks(Scale(pic.img, cols, 2*rows)), cols, rows)
}

// setRows keeps an image's encoded rows, and drops those of the image shown
// longest ago once more than rowsKept images hold some.
func (s *Store) setRows(e *entry, rows []string, cols, gridRows int) {
	e.rows, e.rowsCols, e.rowsRows = rows, cols, gridRows
	s.withRows[e] = struct{}{}
	if len(s.withRows) <= rowsKept {
		return
	}
	var oldest *entry
	for c := range s.withRows {
		if c != e && (oldest == nil || c.shown < oldest.shown) {
			oldest = c
		}
	}
	s.dropRows(oldest)
}

// dropRows forgets an image's encoded rows.
func (s *Store) dropRows(e *entry) {
	if e == nil {
		return
	}
	e.rows = nil
	delete(s.withRows, e)
}

// text returns the text form of an image: the line, and once the file is
// known to be an image, how to save it.
func (s *Store) text(e *entry) Shown {
	out := Shown{Line: TextLine(e.name, e.info, e.bytes, s.ascii)}
	if e.info.W > 0 {
		out.Hint = saveHint(e.name, e.number, s.ascii)
	}
	return out
}

// caption returns the line under an image's picture.
func (s *Store) caption(e *entry) string {
	changed := e.ref.TS > 0 && !e.modified.IsZero() && e.modified.UnixMilli() > e.ref.TS
	return caption(e.name, e.info, e.bytes, e.number, changed, s.ascii)
}

// plan is everything a job needs, copied out of the store when the job is
// made, so that the job reads nothing the update loop writes.
type plan struct {
	owner                *Store // carried to the Done and never read
	serial, epoch, gen   uint64
	key                  entryKey
	path                 string
	limits               Limits
	cellW, cellH         int
	limitCols, limitRows int

	// pic is set when the picture is already here and only has to be sent
	// to the terminal. known is the newest picture of the same path, if
	// any: the fetch asks whether the file still is that one.
	pic, known *picture

	// id is the kitty id to transmit under, at the pixel level; else zero.
	id               int
	maxCols, maxRows int
	tmux             bool
}

// run is the Job.
func (p *plan) run(ctx context.Context, fetch Fetch) (d Done) {
	d = Done{owner: p.owner, serial: p.serial, key: p.key, epoch: p.epoch, gen: p.gen}
	defer func() {
		// The bytes are a model's choice. A decoder that panics on them
		// fails this one image and not the client.
		if recover() != nil {
			d.pic, d.sent, d.fail = nil, nil, failUnreadable
		}
	}()
	pic := p.pic
	switch {
	case pic == nil:
		pic, d.loaded, d.fail = p.load(ctx, fetch)
		if d.fail != 0 {
			return d
		}
	case p.id != 0 && pic.coarse(p.cellW, p.cellH, p.limitCols, p.limitRows):
		// The master was made when cells were smaller. The file is read
		// again for the pixels the grid now has room for; if that fails,
		// the master is still the picture.
		if better, _, fail := p.load(ctx, fetch); fail == 0 {
			pic, d.loaded = better, true
		}
	}
	d.pic = pic
	if p.id != 0 {
		cols, rows := Fit(pic.info.W, pic.info.H, p.cellW, p.cellH, p.maxCols, p.maxRows)
		raw, err := KittyTransmit(p.id, Scale(pic.img, cols*p.cellW, rows*p.cellH), cols, rows, p.tmux)
		if err != nil {
			d.pic, d.fail = nil, failUnreadable
			return d
		}
		d.sent = &placement{id: p.id, cols: cols, rows: rows, raw: raw}
	}
	return d
}

// load fetches the file and makes its master. fresh is false when the
// server said the file is still the picture already known.
func (p *plan) load(ctx context.Context, fetch Fetch) (pic *picture, fresh bool, fail failure) {
	if fetch == nil {
		return nil, false, failUnreadable
	}
	etag := ""
	if p.known != nil {
		etag = p.known.etag
	}
	file, err := fetch(ctx, p.path, etag)
	if err != nil {
		return nil, false, failureOf(err)
	}
	if file.NotModified {
		if p.known == nil {
			return nil, false, failUnreadable
		}
		return p.known, false, 0
	}
	dctx, cancel := context.WithTimeout(ctx, decodeTime)
	defer cancel()
	img, info, err := decode(dctx, file.Data, p.limits)
	if err != nil {
		return nil, false, failureOf(err)
	}
	return &picture{
		img:      masterBox(img, p.cellW, p.cellH, p.limitCols, p.limitRows),
		info:     info,
		bytes:    int64(len(file.Data)),
		etag:     file.ETag,
		modified: file.Modified,
	}, true, 0
}

// failureOf turns an error from a Fetch or from Decode into the line it
// gets.
func failureOf(err error) failure {
	switch {
	case errors.Is(err, ErrNotFound):
		return failNotFound
	case errors.Is(err, ErrTooLarge):
		return failTooLarge
	case errors.Is(err, ErrFormat):
		return failFormat
	}
	return failUnreadable
}

// masterBox retains enough pixels for the configured image budget.
func masterBox(img image.Image, cellW, cellH, cols, rows int) *image.RGBA {
	b := img.Bounds()
	w, h := masterBoxSize(b.Dx(), b.Dy(), cellW, cellH, cols, rows)
	return Scale(img, w, h)
}

// masterBoxSize never exceeds masterSide pixels or enlarges the original.
func masterBoxSize(w, h, cellW, cellH, cols, rows int) (mw, mh int) {
	maxW, maxH := min(cols*cellW, masterSide), min(rows*cellH, masterSide)
	if w <= maxW && h <= maxH {
		return w, h
	}
	if int64(w)*int64(maxH) > int64(h)*int64(maxW) {
		return maxW, max(int(int64(h)*int64(maxW)/int64(w)), 1)
	}
	return max(int(int64(w)*int64(maxH)/int64(h)), 1), maxH
}

// coarse reports whether the master holds fewer pixels than one made from
// the same file at this cell size would: it was scaled down for smaller
// cells.
func (p *picture) coarse(cellW, cellH, cols, rows int) bool {
	w, h := masterBoxSize(p.info.W, p.info.H, cellW, cellH, cols, rows)
	b := p.img.Bounds()
	return w > b.Dx() || h > b.Dy()
}

// cache keeps the most recently used masters, up to a number of bytes.
type cache struct {
	limit, size int
	tick        uint64
	items       map[cacheKey]*cacheItem
	latest      map[string]string // a path's most recently stored ETag
}

// cacheKey identifies one version of one file.
type cacheKey struct{ path, etag string }

type cacheItem struct {
	pic  *picture
	used uint64
}

func newCache(limit int) *cache {
	return &cache{limit: limit, items: make(map[cacheKey]*cacheItem), latest: make(map[string]string)}
}

// get returns the master of one version of a file and marks it used, or nil.
func (c *cache) get(path, etag string) *picture {
	it := c.items[cacheKey{path, etag}]
	if it == nil {
		return nil
	}
	c.tick++
	it.used = c.tick
	return it.pic
}

// newest returns the master of the version of a file stored last, or nil.
func (c *cache) newest(path string) *picture {
	etag, ok := c.latest[path]
	if !ok {
		return nil
	}
	return c.get(path, etag)
}

// put stores a master, then drops the ones used longest ago until the cache
// is within its limit. A master larger than the whole limit is not stored.
func (c *cache) put(path string, pic *picture) {
	key := cacheKey{path, pic.etag}
	c.drop(key)
	n := len(pic.img.Pix)
	if n > c.limit {
		return
	}
	c.tick++
	c.items[key] = &cacheItem{pic: pic, used: c.tick}
	c.latest[path] = pic.etag
	c.size += n
	for c.size > c.limit {
		var oldest cacheKey
		var at uint64
		for k, it := range c.items {
			if at == 0 || it.used < at {
				oldest, at = k, it.used
			}
		}
		c.drop(oldest)
	}
}

// drop removes one master, if it is there.
func (c *cache) drop(key cacheKey) {
	it := c.items[key]
	if it == nil {
		return
	}
	c.size -= len(it.pic.img.Pix)
	delete(c.items, key)
	if c.latest[key.path] == key.etag {
		delete(c.latest, key.path)
	}
}
