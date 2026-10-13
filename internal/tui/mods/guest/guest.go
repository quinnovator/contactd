//go:build wasip1

// Package guest is the mod's side of ABI 1: the bodies of the two functions
// a mod exports. A mod written in Go is a main package with
//
//	//go:wasmexport mod_alloc
//	func modAlloc(n uint32) uint32 { return guest.Alloc(n) }
//
//	//go:wasmexport mod_call
//	func modCall(ptr, n uint32) uint64 { return guest.Call(ptr, n, handle) }
//
//	func main() {}
//
// and a handle function that answers requests. Build it as a reactor:
//
//	GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -buildmode=c-shared
//
// A call is two steps. The host asks mod_alloc for room and writes the JSON
// request there, then calls mod_call, which returns where the JSON reply is
// and how long. Both buffers stay as they are until the next call, which is
// when the host has finished reading.
//
// The JSON is read and written by the code in json.go, not by encoding/json,
// which is several times too slow inside a module. A mod that wants its
// calls to be fast keeps encoding/json and fmt out of its own code too.
package guest

import (
	"unsafe"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// MaxMessage is the largest request or reply the host accepts, in bytes.
const MaxMessage = 64 << 10

// Handler answers one request. The request's Op says what is asked. A
// handler leaves ABI alone, and answers an Op it does not know with an empty
// reply.
type Handler func(req wire.Request) wire.Reply

// The two buffers of the ABI, and the decoder. The buffers are package
// variables so that the garbage collector keeps them while the host reads
// and writes them between calls.
//
//nolint:gochecknoglobals // the ABI needs the buffers to outlive the call that made them
var (
	// request is where the host writes the next request.
	request []byte
	// reply is the last reply, kept until the next call. The next reply
	// is written over it.
	reply []byte
	// reader reads every request. It is kept because it remembers the
	// short strings it has read, and a request repeats them.
	reader decoder
)

// Alloc is the body of the mod_alloc export. It returns the address of n
// bytes for the host to write a request to. They stay valid until the next
// Alloc.
func Alloc(n uint32) uint32 {
	if uint32(cap(request)) < max(n, 1) { //nolint:gosec // a slice in a 32-bit memory has a 32-bit capacity
		request = make([]byte, max(n, 1))
	}
	request = request[:n]
	return address(request)
}

// Call is the body of the mod_call export. ptr and n say where the request
// is, which must be what Alloc last handed out. Call decodes the request,
// runs h, sets the reply's ABI and encodes it. It returns the reply's
// address in the high 32 bits and its length in the low 32.
//
// A request that is not where Alloc put it, or is not the JSON of a request,
// is answered with an error reply and h is not run. So is a reply over
// MaxMessage bytes.
//
// A request is read as encoding/json would read it into a wire.Request: a
// key Call does not know is skipped, and a value of the wrong kind is
// refused. The one difference is that a key counts only as package wire
// spells it, in lower case. What h is handed is its own to keep.
func Call(ptr, n uint32, h Handler) uint64 {
	var out wire.Reply
	var req wire.Request
	switch {
	// The address is compared, never turned back into a pointer: the guest
	// reads only the buffer it owns, whatever the host passes.
	case cap(request) == 0 || ptr != address(request) || int(n) > len(request):
		out.Error = "the request is not where mod_alloc put it"
	case !reader.decode(request[:n], &req):
		out.Error = "the request is not JSON"
	default:
		out = h(req)
	}
	if cap(reply) > 2*MaxMessage {
		// One reply that was far too long should not hold its memory
		// for good.
		reply = nil
	}
	reply = encode(reply[:0], out)
	if len(reply) > MaxMessage {
		reply = encode(reply[:0], wire.Reply{Error: "the reply is over 64 KiB"})
	}
	return uint64(address(reply))<<32 | uint64(len(reply))
}

// encode writes a reply as JSON into buf, with the ABI set.
func encode(buf []byte, r wire.Reply) []byte {
	r.ABI = wire.ABI
	return appendReply(buf, &r)
}

// address is where a buffer's bytes start in the module's memory. Go's
// collector does not move them.
func address(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))) //nolint:gosec // wasm addresses are 32 bits
}
