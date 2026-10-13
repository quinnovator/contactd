// This file is the JSON of ABI 1 as a guest reads and writes it: a decoder
// for a request and an encoder for a reply, written by hand for the types in
// package wire. It uses no reflection.
//
// It is here because encoding/json is too slow inside a module. The host
// must be able to stop a module that never returns, so the sandbox checks
// for a stop at every jump a Go module takes, and a check costs about what
// forty additions do. Reflection jumps all the time: with encoding/json, 50
// model-call records took 2.7 ms to decode, over half a mod's budget. The
// code below reads them in about 0.1 ms. It is written to take few jumps:
//
//   - It looks at a string eight bytes at a time, as one number.
//   - It reads a number eight digits at a time.
//   - It first looks for each key where the host writes it, so that the
//     usual request is read without a search.
//   - It reads an escape without a call, and checks that text outside
//     ASCII is UTF-8 with one jump for each character.
//   - In each function the usual case comes first and runs straight
//     through, and the rest is in a function of its own.
//
// Unlike the rest of the package this file builds on every platform, so that
// its tests run where the host's do, beside encoding/json.

package guest

import (
	"math"
	"math/bits"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// maxDepth is how deep objects and arrays may nest. It is encoding/json's
// limit.
const maxDepth = 10000

// The most elements the host sends in each list of a request. A list is
// given that much room at once; a longer one still fits, by growing.
const (
	roomModelCalls = 50
	roomMessages   = 20
	roomArgs       = 4
)

// decoder reads requests, one at a time. It is kept between requests for the
// two things worth keeping: buf and seen. The zero value is ready.
//
// Its methods read one value each, from where the decoder is, and set bad
// when the input is wrong. After that nothing they read counts.
type decoder struct {
	b []byte
	i int
	// depth counts the objects and arrays that are open.
	depth int
	// fresh says the object or array that was just opened has had no
	// member read yet.
	fresh bool
	// bad says the input is wrong.
	bad bool
	// key is the key member last read. It holds until the next string.
	key []byte
	// searched counts the keys member has read: the members that were not
	// where, or not written as, the host writes them.
	searched int
	// buf is where a string with an escape in it is put together.
	buf []byte
	// seen holds short strings read before. A request says the same few
	// things over and over, a route above all, and a string found here is
	// one that need not be made.
	seen [16]string
}

// decode reads the JSON of one request into req, which should be empty, and
// reports whether data was that. When it was not, req is left half read.
//
// It accepts what encoding/json accepts for a wire.Request and gives the
// same value, with one difference: a key must be spelled as package wire
// spells it, in lower case. A key it does not know is skipped, whatever its
// value. A value of the wrong kind, a number that does not fit, and anything
// that is not JSON are refused. A null clears a list or a part that may be
// absent, and leaves anything else as it was.
func (d *decoder) decode(data []byte, req *wire.Request) bool {
	d.b, d.i, d.depth, d.fresh, d.bad, d.key, d.searched = data, 0, 0, false, false, nil, 0
	d.request(req)
	ok := !d.bad && d.next() == 0 && d.i == len(d.b)
	d.b, d.key = nil, nil
	return ok
}

// next returns the next byte that is not white space, without reading it. It
// returns 0 at the end of the input.
func (d *decoder) next() byte {
	if d.i < len(d.b) {
		if c := d.b[d.i]; c > ' ' {
			return c
		}
		return d.space()
	}
	return 0
}

// space is the slow half of next: it reads white space.
func (d *decoder) space() byte {
	for d.i < len(d.b) {
		c := d.b[d.i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return c
		}
		d.i++
	}
	return 0
}

// literal reads one of the words true, false and null.
func (d *decoder) literal(word string) {
	if len(d.b)-d.i < len(word) || string(d.b[d.i:d.i+len(word)]) != word {
		d.bad = true
		return
	}
	d.i += len(word)
}

// null reports whether the next value is null, and reads it when it is.
func (d *decoder) null() bool {
	if d.next() != 'n' {
		return false
	}
	d.literal("null")
	return true
}

// enter reads the start of an object, when open is '{', or of an array, when
// it is '['. It reports false for a null, which is read, and for anything
// else, which is wrong.
func (d *decoder) enter(open byte) bool {
	if d.i < len(d.b) && d.b[d.i] == open && d.depth < maxDepth {
		d.i++
		d.depth++
		d.fresh = true
		return true
	}
	return d.enterSlowly(open)
}

// enterSlowly is enter for all but the usual case.
func (d *decoder) enterSlowly(open byte) bool {
	if d.next() != open {
		if !d.null() {
			d.bad = true
		}
		return false
	}
	d.i++
	d.depth++
	if d.depth > maxDepth {
		d.bad = true
		return false
	}
	d.fresh = true
	return true
}

// member moves to the next member of the open object and reads its key into
// d.key, leaving the decoder at the member's value. It reports false at the
// end of the object, which it reads, and when the input is wrong.
//
// It reads a key however it is written. When the member is the one a caller
// expects, quick is the faster way to the same place.
func (d *decoder) member() bool {
	if d.i < len(d.b) && d.b[d.i] == '}' && !d.bad {
		d.i++
		d.depth--
		d.fresh = false
		return false
	}
	return d.search()
}

// search is member for all but the usual case, which is that every member
// has been read by quick and the object ends.
func (d *decoder) search() bool {
	if d.bad {
		return false
	}
	c := d.next()
	first := d.fresh
	d.fresh = false
	if c == '}' {
		d.i++
		d.depth--
		return false
	}
	if !first {
		if c != ',' {
			d.bad = true
			return false
		}
		d.i++
		c = d.next()
	}
	if c != '"' {
		d.bad = true
		return false
	}
	d.key = d.text()
	d.searched++
	if d.bad || d.next() != ':' {
		d.bad = true
		return false
	}
	d.i++
	return true
}

// quick reads the start of the next member of the open object, up to its
// value, when that is exactly pat: a key in its quotes and the colon, with no
// white space, as the host writes it. Otherwise it reads nothing and reports
// false, and the member is left for member to read. pat is 4 to 16 bytes.
//
// It compares four bytes at a time, where comparing strings would loop over
// single bytes.
func (d *decoder) quick(pat string) bool {
	b, i, n := d.b, d.i, len(pat)
	if len(b)-i <= n {
		return false
	}
	if !d.fresh {
		if b[i] != ',' {
			return false
		}
		i++
	}
	// Four bytes at four places cover any length from 4 to 16: the first
	// four, the last four, and the four after and before those, which are
	// the same ones again in a short pattern. short is n-8 when the
	// pattern is under eight bytes and 0 otherwise, worked out without a
	// jump.
	at := b[i : i+n]
	short := (n - 8) & ((n - 8) >> 63)
	second, third := 4+short, n-8-short
	differ := (half(at) ^ halfOf(pat)) | (half(at[second:]) ^ halfOf(pat[second:])) |
		(half(at[third:]) ^ halfOf(pat[third:])) | (half(at[n-4:]) ^ halfOf(pat[n-4:]))
	if differ == 0 {
		d.i, d.fresh = i+n, false
		return true
	}
	return false
}

// element moves to the next element of the open array. It reports false at
// the end of the array, which it reads, and when the input is wrong.
func (d *decoder) element() bool {
	if d.i < len(d.b) && d.b[d.i] == ',' && !d.fresh && !d.bad {
		d.i++
		return true
	}
	return d.elementSlowly()
}

// elementSlowly is element for all but the usual case, which is a comma.
func (d *decoder) elementSlowly() bool {
	if d.bad {
		return false
	}
	c := d.next()
	first := d.fresh
	d.fresh = false
	if c == ']' {
		d.i++
		d.depth--
		return false
	}
	if first {
		return true
	}
	if c != ',' {
		d.bad = true
		return false
	}
	d.i++
	return true
}

// ---- the types of package wire ---------------------------------------------

// Each type is read the same way. First come its members as the host writes
// them, in the host's order: a line reads its member when that member is
// next, and otherwise does nothing. Then a loop reads whatever is left, in
// any order and any form. The loop alone would be enough, and the lines
// above it only save it the search for each key.

func (d *decoder) request(r *wire.Request) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"abi":`) {
		d.int(&r.ABI)
	}
	if d.quick(`"op":`) {
		d.str(&r.Op)
	}
	if d.quick(`"name":`) {
		d.str(&r.Name)
	}
	if d.quick(`"args":`) {
		list(d, &r.Args, (*decoder).str, roomArgs)
	}
	if d.quick(`"width":`) {
		d.int(&r.Width)
	}
	if d.quick(`"term":`) {
		d.term(&r.Term)
	}
	if d.quick(`"data":`) {
		d.data(&r.Data)
	}
	if d.quick(`"item":`) {
		pointer(d, &r.Item, (*decoder).item)
	}
	for d.member() {
		switch string(d.key) {
		case "abi":
			d.int(&r.ABI)
		case "op":
			d.str(&r.Op)
		case "name":
			d.str(&r.Name)
		case "args":
			list(d, &r.Args, (*decoder).str, roomArgs)
		case "width":
			d.int(&r.Width)
		case "term":
			d.term(&r.Term)
		case "data":
			d.data(&r.Data)
		case "item":
			pointer(d, &r.Item, (*decoder).item)
		default:
			d.skip()
		}
	}
}

func (d *decoder) term(t *wire.Term) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"color":`) {
		d.str(&t.Color)
	}
	if d.quick(`"glyphs":`) {
		d.str(&t.Glyphs)
	}
	if d.quick(`"background":`) {
		d.str(&t.Background)
	}
	for d.member() {
		switch string(d.key) {
		case "color":
			d.str(&t.Color)
		case "glyphs":
			d.str(&t.Glyphs)
		case "background":
			d.str(&t.Background)
		default:
			d.skip()
		}
	}
}

func (d *decoder) data(v *wire.Data) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"contact":`) {
		pointer(d, &v.Contact, (*decoder).contact)
	}
	if d.quick(`"route":`) {
		pointer(d, &v.Route, (*decoder).route)
	}
	if d.quick(`"state":`) {
		pointer(d, &v.State, (*decoder).state)
	}
	if d.quick(`"model_calls":`) {
		list(d, &v.ModelCalls, (*decoder).modelCall, roomModelCalls)
	}
	if d.quick(`"messages":`) {
		list(d, &v.Messages, (*decoder).message, roomMessages)
	}
	if d.quick(`"workspace":`) {
		pointer(d, &v.Workspace, (*decoder).workspace)
	}
	if d.quick(`"now":`) {
		d.int64(&v.Now)
	}
	for d.member() {
		switch string(d.key) {
		case "contact":
			pointer(d, &v.Contact, (*decoder).contact)
		case "route":
			pointer(d, &v.Route, (*decoder).route)
		case "state":
			pointer(d, &v.State, (*decoder).state)
		case "model_calls":
			list(d, &v.ModelCalls, (*decoder).modelCall, roomModelCalls)
		case "messages":
			list(d, &v.Messages, (*decoder).message, roomMessages)
		case "workspace":
			pointer(d, &v.Workspace, (*decoder).workspace)
		case "now":
			d.int64(&v.Now)
		default:
			d.skip()
		}
	}
}

func (d *decoder) contact(c *wire.Contact) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"id":`) {
		d.uint64(&c.ID)
	}
	if d.quick(`"name":`) {
		d.str(&c.Name)
	}
	for d.member() {
		switch string(d.key) {
		case "id":
			d.uint64(&c.ID)
		case "name":
			d.str(&c.Name)
		default:
			d.skip()
		}
	}
}

func (d *decoder) route(r *wire.Route) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"provider":`) {
		d.str(&r.Provider)
	}
	if d.quick(`"model":`) {
		d.str(&r.Model)
	}
	for d.member() {
		switch string(d.key) {
		case "provider":
			d.str(&r.Provider)
		case "model":
			d.str(&r.Model)
		default:
			d.skip()
		}
	}
}

func (d *decoder) state(s *wire.State) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"conn":`) {
		d.str(&s.Conn)
	}
	if d.quick(`"replying":`) {
		d.bool(&s.Replying)
	}
	if d.quick(`"running":`) {
		d.str(&s.Running)
	}
	for d.member() {
		switch string(d.key) {
		case "conn":
			d.str(&s.Conn)
		case "replying":
			d.bool(&s.Replying)
		case "running":
			d.str(&s.Running)
		default:
			d.skip()
		}
	}
}

func (d *decoder) modelCall(c *wire.ModelCall) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"seq":`) {
		d.uint64(&c.Seq)
	}
	if d.quick(`"ts":`) {
		d.int64(&c.TS)
	}
	if d.quick(`"step":`) {
		d.int(&c.Step)
	}
	if d.quick(`"route":`) {
		d.str(&c.Route)
	}
	if d.quick(`"input":`) {
		d.int(&c.Input)
	}
	if d.quick(`"cached":`) {
		d.int(&c.Cached)
	}
	if d.quick(`"output":`) {
		d.int(&c.Output)
	}
	if d.quick(`"reasoning":`) {
		d.int(&c.Reasoning)
	}
	if d.quick(`"ms":`) {
		d.int64(&c.MS)
	}
	if d.quick(`"error":`) {
		d.str(&c.Error)
	}
	for d.member() {
		switch string(d.key) {
		case "seq":
			d.uint64(&c.Seq)
		case "ts":
			d.int64(&c.TS)
		case "step":
			d.int(&c.Step)
		case "route":
			d.str(&c.Route)
		case "input":
			d.int(&c.Input)
		case "cached":
			d.int(&c.Cached)
		case "output":
			d.int(&c.Output)
		case "reasoning":
			d.int(&c.Reasoning)
		case "ms":
			d.int64(&c.MS)
		case "error":
			d.str(&c.Error)
		default:
			d.skip()
		}
	}
}

func (d *decoder) message(m *wire.Message) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"seq":`) {
		d.uint64(&m.Seq)
	}
	if d.quick(`"ts":`) {
		d.int64(&m.TS)
	}
	if d.quick(`"role":`) {
		d.str(&m.Role)
	}
	if d.quick(`"text":`) {
		d.str(&m.Text)
	}
	for d.member() {
		switch string(d.key) {
		case "seq":
			d.uint64(&m.Seq)
		case "ts":
			d.int64(&m.TS)
		case "role":
			d.str(&m.Role)
		case "text":
			d.str(&m.Text)
		default:
			d.skip()
		}
	}
}

func (d *decoder) workspace(w *wire.Workspace) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"dir":`) {
		d.str(&w.Dir)
	}
	if d.quick(`"branch":`) {
		d.str(&w.Branch)
	}
	for d.member() {
		switch string(d.key) {
		case "dir":
			d.str(&w.Dir)
		case "branch":
			d.str(&w.Branch)
		default:
			d.skip()
		}
	}
}

func (d *decoder) item(it *wire.Item) {
	if !d.enter('{') {
		return
	}
	if d.quick(`"kind":`) {
		d.str(&it.Kind)
	}
	if d.quick(`"text":`) {
		d.str(&it.Text)
	}
	for d.member() {
		switch string(d.key) {
		case "kind":
			d.str(&it.Kind)
		case "text":
			d.str(&it.Text)
		default:
			d.skip()
		}
	}
}

// pointer reads a value that may be absent. A null clears *p. Anything else
// is read by read into what *p points to, which is made when there is
// nothing.
func pointer[T any](d *decoder, p **T, read func(*decoder, *T)) {
	if d.null() {
		*p = nil
		return
	}
	if *p == nil {
		*p = new(T)
	}
	read(d, *p)
}

// list reads an array, each element by read. A null clears *p, and an empty
// array leaves it empty and not nil. A new list starts with room for room
// elements.
//
// When *p already has elements, because the key came twice, each one is read
// over what is there, and that goes for what an earlier, longer list left
// behind its end as well. It is what encoding/json does.
func list[T any](d *decoder, p *[]T, read func(*decoder, *T), room int) {
	if !d.enter('[') {
		*p = nil
		return
	}
	// s is all the memory the list has, used or not.
	s, n := *p, 0
	s = s[:cap(s)]
	for d.element() {
		if n < len(s) {
			read(d, &s[n])
			n++
			continue
		}
		s = extend(s, room)
		read(d, &s[n])
		n++
	}
	if n == 0 {
		s = []T{}
	}
	*p = s[:n]
}

// extend gives a full list more room: room elements when it has none, and
// at least one more otherwise.
func extend[T any](s []T, room int) []T {
	if cap(s) == 0 {
		return make([]T, max(room, 1))
	}
	var zero T
	s = append(s, zero)
	return s[:cap(s)]
}

// ---- values ----------------------------------------------------------------

func (d *decoder) str(p *string) {
	if d.i >= len(d.b) || d.b[d.i] != '"' {
		d.strSlowly(p)
		return
	}
	t := d.text()
	if d.bad {
		return
	}
	*p = d.intern(t)
}

// strSlowly is str for all but the usual case, which is a string with no
// white space before it.
func (d *decoder) strSlowly(p *string) {
	if d.next() != '"' {
		if !d.null() {
			d.bad = true
		}
		return
	}
	if t := d.text(); !d.bad {
		*p = d.intern(t)
	}
}

// intern returns t as a string: one it has returned before when it can, and
// a new one otherwise. Only short strings are looked for. A long one is the
// text of a message, which is not said twice.
func (d *decoder) intern(t []byte) string {
	n := len(t)
	if n == 0 {
		return ""
	}
	if n > 32 {
		return string(t)
	}
	slot := &d.seen[(int(t[0])+7*int(t[n-1])+13*n)%len(d.seen)]
	if len(*slot) == n && same(t, *slot) {
		return *slot
	}
	return remember(slot, t)
}

// remember puts t in a slot of seen.
func remember(slot *string, t []byte) string {
	*slot = string(t)
	return *slot
}

// same reports whether t and s hold the same bytes. They are equally long,
// and at most 32 bytes.
//
// From eight bytes up it compares four words and takes no jump: the first
// two, the last two, which are the same ones again in a string under sixteen
// bytes.
func same(t []byte, s string) bool {
	n := len(t)
	// A length under eight wraps to a huge number, so this is one test.
	if uint(n-8) <= 32-8 && len(s) == n {
		// short is n-16 when there are under sixteen bytes and 0
		// otherwise.
		short := (n - 16) & ((n - 16) >> 63)
		second, third := 8+short, n-16-short
		return (word(t)^wordOf(s))|(word(t[second:])^wordOf(s[second:]))|
			(word(t[third:])^wordOf(s[third:]))|(word(t[n-8:])^wordOf(s[n-8:])) == 0
	}
	return string(t) == s
}

func (d *decoder) bool(p *bool) {
	switch d.next() {
	case 't':
		d.literal("true")
		*p = true
	case 'f':
		d.literal("false")
		*p = false
	case 'n':
		d.literal("null")
	default:
		d.bad = true
	}
}

func (d *decoder) int(p *int) {
	if v, ok := d.count(math.MaxInt); ok {
		*p = int(v) //nolint:gosec // count kept it under the limit
		return
	}
	d.intSlowly(p)
}

func (d *decoder) int64(p *int64) {
	if v, ok := d.count(math.MaxInt64); ok {
		*p = int64(v) //nolint:gosec // count kept it under the limit
		return
	}
	if !d.null() {
		d.signed(p)
	}
}

func (d *decoder) uint64(p *uint64) {
	if v, ok := d.count(math.MaxUint64); ok {
		*p = v
		return
	}
	d.uint64Slowly(p)
}

func (d *decoder) intSlowly(p *int) {
	var v int64
	if d.null() || !d.signed(&v) {
		return
	}
	if v < math.MinInt || v > math.MaxInt {
		d.bad = true
		return
	}
	*p = int(v)
}

func (d *decoder) uint64Slowly(p *uint64) {
	if d.null() {
		return
	}
	// A minus sign is refused even before a zero, as strconv.ParseUint
	// refuses it.
	below, size := d.whole()
	if below {
		d.bad = true
	}
	if !d.bad {
		*p = size
	}
}

// count reads a whole number with no sign, fewer than sixteen digits and a
// value of at most limit, which is nearly every number in a request, eight
// digits at a time. For anything else it reads nothing and reports false,
// and the number is left for whole, which reads a digit at a time.
func (d *decoder) count(limit uint64) (uint64, bool) {
	b, i := d.b, d.i
	if len(b)-i <= 16 {
		return 0, false
	}
	w := word(b[i : i+8])
	n, v := digits(w)
	if n == 8 {
		return d.longCount(v, limit)
	}
	// JSON has no zero before another digit. alone is 1 for a number of
	// one digit and 0 for a longer one, so the middle test is of both at
	// once and needs one jump where two tests would need two.
	alone := uint64(n-2) >> 63 //nolint:gosec // the sign bit of n-2 is the point
	if c := b[i+n]; n != 0 && (w^'0')&0xFF|alone != 0 && c != '.' && c|0x20 != 'e' && v <= limit {
		d.i = i + n
		return v, true
	}
	return 0, false
}

// longCount is count for a number of eight digits or more. first is the
// value of its first eight.
func (d *decoder) longCount(first, limit uint64) (uint64, bool) {
	// 1, 10, 100 and so on, to shift the first eight up by the digits that
	// follow them.
	tens := [8]uint64{1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000}
	b, i := d.b, d.i
	n, low := digits(word(b[i+8 : i+16]))
	if n == 8 || b[i] == '0' {
		return 0, false
	}
	v := first*tens[n] + low
	if c := b[i+8+n]; c == '.' || c|0x20 == 'e' || v > limit {
		return 0, false
	}
	d.i = i + 8 + n
	return v, true
}

// digits reads the digits at the start of a word of eight characters: how
// many there are, and their value.
func digits(w uint64) (n int, v uint64) {
	// A digit is now 0 to 9, and adding 0x76 to anything more sets the top
	// bit of its byte.
	w ^= ones * '0'
	n = bits.TrailingZeros64((w+ones*0x76|w)&highs) / 8
	// Move the digits to the top of the word, so that zeros come in below
	// them and what followed them falls off. Shifting by 64 leaves zero.
	w <<= uint(8-n) * 8
	// Every other byte becomes the number its two digits make. Those four
	// numbers are then weighted and added in the top half of two products:
	// the first and third in one, the second and fourth in the other.
	w = w*10 + w>>8
	const (
		pairs       = 0x000000FF000000FF
		firstThird  = 1_000_000<<32 + 100
		secondForth = 10_000<<32 + 1
	)
	v = ((w&pairs)*firstThird + (w>>16&pairs)*secondForth) >> 32
	return n, v
}

// signed reads a whole number that may be below zero.
func (d *decoder) signed(p *int64) bool {
	below, size := d.whole()
	switch {
	case d.bad:
		return false
	case !below && size <= math.MaxInt64:
		*p = int64(size)
	case below && size <= 1<<63:
		// Wrapping gives the right answer for the smallest number too.
		*p = -int64(size) //nolint:gosec // size is at most 1<<63, checked above
	default:
		d.bad = true
		return false
	}
	return true
}

// whole reads a number with no fraction and no exponent: whether it has a
// minus sign, and its size. A number that has either, or that does not fit
// in 64 bits, is wrong: no field of a request can hold it.
func (d *decoder) whole() (below bool, size uint64) {
	// The first size that cannot be multiplied by ten.
	const tooBig = math.MaxUint64/10 + 1
	d.next()
	b, i := d.b, d.i
	if i < len(b) && b[i] == '-' {
		below = true
		i++
	}
	first := i
	for i < len(b) {
		digit := b[i] - '0'
		if digit > 9 {
			break
		}
		if size >= tooBig {
			d.bad = true
			return false, 0
		}
		size *= 10
		if size+uint64(digit) < size {
			d.bad = true
			return false, 0
		}
		size += uint64(digit)
		i++
	}
	switch {
	case i == first:
		// Not a number at all.
		d.bad = true
	case b[first] == '0' && i-first > 1:
		// JSON has no leading zero.
		d.bad = true
	case i < len(b) && (b[i] == '.' || b[i]|0x20 == 'e'):
		d.bad = true
	}
	d.i = i
	return below, size
}

// skip reads one value of any kind and keeps nothing of it. It is as strict
// as the rest: what it skips must be JSON.
func (d *decoder) skip() {
	switch c := d.next(); c {
	case '"':
		d.text()
	case '{':
		d.enter('{')
		for d.member() {
			d.skip()
		}
	case '[':
		d.enter('[')
		for d.element() {
			d.skip()
		}
	case 't':
		d.literal("true")
	case 'f':
		d.literal("false")
	case 'n':
		d.literal("null")
	default:
		d.number()
	}
}

// number reads a number of any form JSON has.
func (d *decoder) number() {
	b, i := d.b, d.i
	digits := func() bool {
		from := i
		for i < len(b) && b[i]-'0' <= 9 {
			i++
		}
		return i > from
	}
	if i < len(b) && b[i] == '-' {
		i++
	}
	first := i
	if !digits() || b[first] == '0' && i-first > 1 {
		d.bad = true
		return
	}
	if i < len(b) && b[i] == '.' {
		i++
		if !digits() {
			d.bad = true
			return
		}
	}
	if i < len(b) && b[i]|0x20 == 'e' {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if !digits() {
			d.bad = true
			return
		}
	}
	d.i = i
}

// ---- strings ---------------------------------------------------------------

// text reads a string, from its opening quote, and returns its characters.
// They are a part of the input when the string is plain ASCII with no escape
// in it, which is the usual case and copies nothing; otherwise they are in
// d.buf. Either way they hold only until the next string is read.
func (d *decoder) text() []byte {
	b, start := d.b, d.i+1
	i := scan(b, start, highs)
	if i < len(b) && b[i] == '"' {
		d.i = i + 1
		return b[start:i]
	}
	return d.mixed(start, i)
}

// mixed is the slow half of text: a string that has an escape or a character
// outside ASCII in it, or is wrong. The bytes from start up to i are plain.
func (d *decoder) mixed(start, i int) []byte {
	b := d.b
	buf := append(d.buf[:0], b[start:i]...)
	for i < len(b) {
		// An escape, when it is not in the last few bytes of the input.
		// Text is full of two of them, the newline and the quote, and a
		// control character is written as the long kind, so both kinds
		// are read here, without a call. unescape, below, reads what is
		// left: half of a pair, an escape that is wrong, and one at the
		// very end of the input.
		if b[i] == '\\' && i+6 <= len(b) {
			e := b[i+1]
			if c := escaped[e]; c != 0 {
				buf = append(buf, c)
				i += 2
				// And the plain text after it, unless the next thing is
				// another escape.
				if b[i] != '\\' {
					j := scan(b, i, highs)
					buf = append(buf, b[i:j]...)
					i = j
				}
				continue
			}
			// The four digits of the long kind, each one less than the
			// table holds, so that a byte that is no digit has its top
			// bit set.
			x0, x1, x2, x3 := hexPlus[b[i+2]]-1, hexPlus[b[i+3]]-1, hexPlus[b[i+4]]-1, hexPlus[b[i+5]]-1
			r := rune(x0)<<12 | rune(x1)<<8 | rune(x2)<<4 | rune(x3)
			if e == 'u' && (x0|x1|x2|x3)&0x80 == 0 && (r < 0xD800 || r >= 0xE000) {
				if r < utf8.RuneSelf {
					// The first two digits are zeros.
					buf = append(buf, x2<<4|x3)
				} else {
					buf = utf8.AppendRune(buf, r)
				}
				i += 6
				continue
			}
		}
		switch c := b[i]; {
		case c == '"':
			d.i, d.buf = i+1, buf
			return buf
		case c == '\\':
			var ok bool
			if buf, i, ok = unescape(buf, b, i); !ok {
				d.bad = true
				return nil
			}
		case c < 0x20:
			// A control character must be written as an escape.
			d.bad = true
			return nil
		case c >= utf8.RuneSelf:
			// Characters outside ASCII, up to the next byte that needs
			// a look. None of their bytes is such a byte, so the run
			// never ends inside a character.
			j := scan(b, i, 0)
			buf = appendUTF8(buf, b, i, j)
			i = j
		}
		j := scan(b, i, highs)
		buf = append(buf, b[i:j]...)
		i = j
	}
	// The string never ends.
	d.bad = true
	return nil
}

// unescape reads the escape at b[i], which is a backslash, and appends the
// character it stands for. It returns where the escape ends.
func unescape(buf, b []byte, i int) ([]byte, int, bool) {
	if i+1 >= len(b) {
		return buf, i, false
	}
	var c byte
	switch b[i+1] {
	case '"', '\\', '/':
		c = b[i+1]
	case 'b':
		c = '\b'
	case 'f':
		c = '\f'
	case 'n':
		c = '\n'
	case 'r':
		c = '\r'
	case 't':
		c = '\t'
	case 'u':
		r := hex4(b, i+2)
		if r < 0 {
			return buf, i, false
		}
		i += 6
		if r >= 0xD800 && r < 0xE000 {
			// Half of a pair. With its other half after it the two are
			// one character; alone it is the replacement character.
			low := rune(-1)
			if r < 0xDC00 && i+1 < len(b) && b[i] == '\\' && b[i+1] == 'u' {
				low = hex4(b, i+2)
			}
			if low >= 0xDC00 && low < 0xE000 {
				r = 0x10000 + (r-0xD800)<<10 + (low - 0xDC00)
				i += 6
			} else {
				r = utf8.RuneError
			}
		}
		return utf8.AppendRune(buf, r), i, true
	default:
		return buf, i, false
	}
	return append(buf, c), i + 2, true
}

// hex4 reads four hex digits at b[i]. It returns -1 when they are not there.
func hex4(b []byte, i int) rune {
	if i+4 > len(b) {
		return -1
	}
	var r rune
	for _, c := range b[i : i+4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c -= 'a' - 10
		case c >= 'A' && c <= 'F':
			c -= 'A' - 10
		default:
			return -1
		}
		r = r<<4 | rune(c)
	}
	return r
}

// appendUTF8 appends b[i:j] with every byte that is not UTF-8 replaced by the
// replacement character. b[i:j] ends where b does, or before a byte of ASCII.
func appendUTF8(buf, b []byte, i, j int) []byte {
	if valid(b, i, j) {
		return append(buf, b[i:j]...)
	}
	for p := b[i:j]; len(p) > 0; {
		r, size := utf8.DecodeRune(p)
		buf = utf8.AppendRune(buf, r)
		p = p[size:]
	}
	return buf
}

// valid reports whether b[i:j] is UTF-8, as utf8.Valid does. b[i:j] must end
// where b does, or before a byte of ASCII that is no part of a character, so
// that a character cut short at j is seen to be.
//
// It takes one jump for each character and none inside one, where utf8.Valid
// takes four or five: text outside ASCII is read several times faster in a
// module for it. What is wrong with a character is gathered in the top bit
// of bad and looked at once, at the end.
func valid(b []byte, i, j int) bool {
	var bad uint32
	// Four bytes are looked at for each character, whatever its length,
	// so the last three bytes of b are left to utf8.Valid.
	for limit := min(j, len(b)-3); i < limit; {
		p := b[i : i+4]
		w, at := half(p), int(lead[p[0]])
		// The second byte must be in a range that the first one sets.
		second := uint32(p[1] - kinds[at+1])
		// The third and the fourth, when the character has them, must
		// be 10xxxxxx. rest is what they are not, and it is zero when
		// they are right.
		more := uint32(kinds[at+3])
		mask := 0x00C00000*(more&1) | 0xC0000000*(more>>1&1)
		rest := w&mask ^ mask&0x80800000
		bad |= uint32(kinds[at+2]) - second | more<<24 | rest | -rest
		i += int(kinds[at])
	}
	if i < j {
		return bad>>31 == 0 && utf8.Valid(b[i:j])
	}
	return bad>>31 == 0
}

// Runs of one byte, to build the tables below from. A table is a string
// constant: it costs nothing to set up, and a byte indexes it with no check.
const (
	zero4   = "\x00\x00\x00\x00"
	zero16  = zero4 + zero4 + zero4 + zero4
	zero64  = zero16 + zero16 + zero16 + zero16
	zero128 = zero64 + zero64
	not4    = "\x04\x04\x04\x04"
	not16   = not4 + not4 + not4 + not4
	two4    = "\x08\x08\x08\x08"
	three4  = "\x10\x10\x10\x10"
)

// escaped is what the character after a backslash stands for, for the eight
// escapes that stand for one character, and 0 for every other byte.
const escaped = zero16 + zero16 +
	"\x00\x00\"" + zero4 + zero4 + zero4 + "/" + // 0x20: the quote and the slash
	zero16 + zero16 +
	zero4 + zero4 + zero4 + "\\\x00\x00\x00" + // 0x50: the backslash
	"\x00\x00\b\x00\x00\x00\f" + zero4 + "\x00\x00\x00\n\x00" + // 0x60: b, f and n
	"\x00\x00\r\x00\t" + zero4 + zero4 + "\x00\x00\x00" + // 0x70: r and t
	zero128

// hexPlus is one more than the value of a hex digit, and 0 for every byte
// that is not one.
const hexPlus = zero16 + zero16 + zero16 +
	"\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x00\x00" + zero4 + // 0 to 9
	"\x00\x0b\x0c\x0d\x0e\x0f\x10\x00" + zero4 + zero4 + // A to F
	zero16 +
	"\x00\x0b\x0c\x0d\x0e\x0f\x10\x00" + zero4 + zero4 + // a to f
	zero16 +
	zero128

// lead is, for the first byte of a character, where in kinds its kind is.
const lead = zero128 + // ASCII
	not16 + not16 + not16 + not16 + // 0x80 to 0xBF: no first byte
	"\x04\x04" + two4 + two4 + two4 + two4 + two4 + two4 + two4 + "\x08\x08" + // 0xC0 and 0xC1: none; 0xC2 to 0xDF: two bytes
	"\x0c" + three4 + three4 + three4 + "\x14\x10\x10" + // 0xE0 to 0xEF: three bytes
	"\x18\x1c\x1c\x1c\x20" + not4 + not4 + "\x04\x04\x04" // 0xF0 to 0xF4: four bytes; then none

// kinds holds four bytes for each kind of first byte: how many bytes the
// character has; the least its second byte may be; how far above that it may
// be; and what follows, where 1 is a third byte, 3 a third and a fourth, and
// 0x80 means the byte starts no character.
const kinds = "\x01\x00\xff\x00" + // ASCII: any byte may follow
	"\x01\x00\xff\x80" + // no first byte
	"\x02\x80\x3f\x00" + // two bytes
	"\x03\xa0\x1f\x01" + // 0xE0: no character that two bytes could hold
	"\x03\x80\x3f\x01" + // three bytes
	"\x03\x80\x1f\x01" + // 0xED: no half of a pair
	"\x04\x90\x2f\x03" + // 0xF0: no character that three bytes could hold
	"\x04\x80\x3f\x03" + // four bytes
	"\x04\x80\x0f\x03" // 0xF4: nothing above U+10FFFF

// One bit pattern in each of the eight bytes of a word.
const (
	ones  = 0x0101010101010101
	highs = 0x8080808080808080
)

// scan returns the index of the first byte of b, at or after i, that a JSON
// string cannot hold as it is: a quote, a backslash or a control character.
// With high set to highs it stops at a byte of a character outside ASCII as
// well; with high 0 it passes those. It returns len(b) when there is none.
//
// It reads eight bytes at a time. Each test below sets the top bit of a byte
// that matches. A test can also mark a byte above the first one it matches,
// never one below, so the lowest mark of all is always right.
func scan(b []byte, i int, high uint64) int {
	for ; i+8 <= len(b); i += 8 {
		w := word(b[i : i+8])
		quote := w ^ ones*'"'
		slash := w ^ ones*'\\'
		control := w - ones*0x20
		marks := (control&^w|(quote-ones)&^quote|(slash-ones)&^slash)&highs | w&high
		if marks != 0 {
			return i + bits.TrailingZeros64(marks)/8
		}
	}
	for ; i < len(b); i++ {
		if c := b[i]; c < 0x20 || c == '"' || c == '\\' || c >= 0x80 && high != 0 {
			return i
		}
	}
	return i
}

// word is the first eight bytes of b as one number, the first byte lowest.
// The compiler makes one load of it.
func word(b []byte) uint64 {
	_ = b[7]
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

// wordOf is word for a string.
func wordOf(s string) uint64 {
	_ = s[7]
	return uint64(s[0]) | uint64(s[1])<<8 | uint64(s[2])<<16 | uint64(s[3])<<24 |
		uint64(s[4])<<32 | uint64(s[5])<<40 | uint64(s[6])<<48 | uint64(s[7])<<56
}

// half is the first four bytes of b as one number, the first byte lowest.
func half(b []byte) uint32 {
	_ = b[3]
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// halfOf is half for a string.
func halfOf(s string) uint32 {
	_ = s[3]
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

// ---- the reply -------------------------------------------------------------

// appendReply appends r to dst as JSON, and returns the longer slice. A
// field that is empty is left out, except abi. What it writes is always
// JSON, whatever the strings hold: a byte that is not UTF-8 becomes the
// replacement character.
func appendReply(dst []byte, r *wire.Reply) []byte {
	dst = append(dst, `{"abi":`...)
	dst = strconv.AppendInt(dst, int64(r.ABI), 10)
	dst = appendField(dst, `,"text":`, r.Text)
	dst = appendField(dst, `,"short":`, r.Short)
	dst = appendField(dst, `,"tone":`, r.Tone)
	if len(r.Lines) > 0 {
		dst = append(dst, `,"lines":[`...)
		for i, line := range r.Lines {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendLine(dst, line)
		}
		dst = append(dst, ']')
	}
	dst = appendField(dst, `,"insert":`, r.Insert)
	dst = appendField(dst, `,"note":`, r.Note)
	if r.Pass {
		dst = append(dst, `,"pass":true`...)
	}
	dst = appendField(dst, `,"error":`, r.Error)
	return append(dst, '}')
}

// appendField appends a key and its string, or nothing for an empty string.
func appendField(dst []byte, key, s string) []byte {
	if s == "" {
		return dst
	}
	return appendString(append(dst, key...), s)
}

func appendLine(dst []byte, line wire.Line) []byte {
	if line == nil {
		// encoding/json writes a nil slice so, and the host reads it back
		// as a line with nothing on it.
		return append(dst, "null"...)
	}
	dst = append(dst, '[')
	for i, span := range line {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendString(append(dst, `{"t":`...), span.T)
		dst = appendField(dst, `,"tone":`, span.Tone)
		if span.Bold {
			dst = append(dst, `,"bold":true`...)
		}
		dst = append(dst, '}')
	}
	return append(dst, ']')
}

// appendString appends s as a JSON string.
func appendString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	// The string's own bytes, to look at eight at a time. Nothing writes
	// to them.
	b := unsafe.Slice(unsafe.StringData(s), len(s)) //nolint:gosec // a read-only view of s, not kept
	dst = append(dst, '"')
	// Until a character outside ASCII turns up the scan stops at one. The
	// first settles the rest of the string: text that is UTF-8 is copied as
	// it is, which is why the scan then passes such characters, and text
	// that is not is gone through a character at a time.
	high, checked := uint64(highs), false
	for i := 0; ; {
		j := scan(b, i, high)
		dst = append(dst, b[i:j]...)
		if j == len(b) {
			return append(dst, '"')
		}
		i = j + 1
		switch c := b[j]; {
		case c == '"' || c == '\\':
			dst = append(dst, '\\', c)
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		case !checked && valid(b, j, len(b)):
			high, i = 0, j
		default:
			// One character, or one byte that is not part of any, which
			// becomes the replacement character.
			checked = true
			r, size := utf8.DecodeRune(b[j:])
			dst = utf8.AppendRune(dst, r)
			i = j + size
		}
	}
}
