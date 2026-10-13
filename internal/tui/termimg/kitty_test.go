package termimg_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// command is one kitty graphics command: its keys and its payload.
type command struct{ keys, payload string }

// commands cuts kitty graphics commands out of bytes meant for the terminal.
// It fails the test when anything else is in them, or a command is not
// terminated.
func commands(t *testing.T, raw string) []command {
	t.Helper()
	var out []command
	for raw != "" {
		if !strings.HasPrefix(raw, "\x1b_G") {
			t.Fatalf("not a kitty graphics command: %q", head(raw))
		}
		end := strings.Index(raw, "\x1b\\")
		if end < 0 {
			t.Fatalf("a command is not terminated: %q", head(raw))
		}
		keys, payload, _ := strings.Cut(raw[3:end], ";")
		if strings.ContainsRune(keys+payload, '\x1b') {
			t.Fatalf("an escape character inside a command: %q", head(raw))
		}
		out = append(out, command{keys: keys, payload: payload})
		raw = raw[end+2:]
	}
	return out
}

// unwrap takes each command out of its tmux passthrough wrapper and returns
// them joined, as the terminal behind tmux receives them.
func unwrap(t *testing.T, raw string) string {
	t.Helper()
	var out strings.Builder
	for raw != "" {
		if !strings.HasPrefix(raw, "\x1bPtmux;") {
			t.Fatalf("not a tmux passthrough: %q", head(raw))
		}
		raw = raw[len("\x1bPtmux;"):]
		// Inside, every ESC is doubled; a single ESC then a backslash ends
		// the wrapper.
		for {
			i := strings.IndexByte(raw, '\x1b')
			if i < 0 || i+1 >= len(raw) {
				t.Fatalf("a passthrough is not terminated: %q", head(raw))
			}
			out.WriteString(raw[:i])
			next := raw[i+1]
			raw = raw[i+2:]
			if next == '\\' {
				break
			}
			if next != '\x1b' {
				t.Fatalf("a lone ESC inside a passthrough, before %q", head(raw))
			}
			out.WriteByte('\x1b')
		}
	}
	return out.String()
}

func head(s string) string { return s[:min(len(s), 60)] }

// shape is a transmit, one command a line, with each payload replaced by a
// word and a run of commands that then look alike written once. A golden
// file of it pins every byte the package chooses and none of the
// compressor's. end is what a command ends with.
func shape(raw, end string, payloads []string) string {
	var out strings.Builder
	prev, repeated := "", false
	for i, part := range strings.SplitAfter(raw, end) {
		if part == "" {
			continue
		}
		if i < len(payloads) {
			part = strings.Replace(part, ";"+payloads[i]+"\x1b", ";<base64>\x1b", 1)
		}
		if part == prev {
			if !repeated {
				out.WriteString("(and so on)\n")
			}
			repeated = true
			continue
		}
		out.WriteString(strconv.QuoteToASCII(part) + "\n")
		prev, repeated = part, false
	}
	return out.String()
}

func TestKittyTransmit(t *testing.T) {
	t.Parallel()
	img := noise(336, 192) // does not compress, so it takes many chunks
	raw, err := termimg.KittyTransmit(7, img, 42, 12, false)
	if err != nil {
		t.Fatalf("KittyTransmit: %v", err)
	}
	cmds := commands(t, raw)
	if len(cmds) < 3 {
		t.Fatalf("%d commands, want a picture that takes at least three", len(cmds))
	}
	var payload strings.Builder
	payloads := make([]string, 0, len(cmds))
	for i, c := range cmds {
		last := i == len(cmds)-1
		want := "m=1"
		if last {
			want = "m=0"
		}
		if i == 0 {
			want = "a=T,U=1,q=2,f=100,t=d,i=7,c=42,r=12," + want
		}
		if c.keys != want {
			t.Errorf("command %d has keys %q, want %q", i, c.keys, want)
		}
		switch {
		case c.payload == "":
			t.Errorf("command %d has no payload", i)
		case len(c.payload) > 4096:
			t.Errorf("command %d has %d bytes of payload, want at most 4096", i, len(c.payload))
		case !last && len(c.payload) != 4096:
			t.Errorf("command %d has %d bytes of payload, want a full 4096", i, len(c.payload))
		case len(c.payload)%4 != 0:
			// The last chunk too: the padding is part of the encoding.
			t.Errorf("command %d has %d bytes of payload, not a multiple of 4", i, len(c.payload))
		}
		payload.WriteString(c.payload)
		payloads = append(payloads, c.payload)
	}

	// The payloads, joined, are a PNG of the very picture.
	file, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatalf("the payload is not base64: %v", err)
	}
	back, err := png.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("the payload is not a PNG: %v", err)
	}
	same(t, termimg.Scale(back, 336, 192), img)
	tuitest.Golden(t, "kitty_transmit.golden", []byte(shape(raw, "\x1b\\", payloads)))

	// Through tmux: the same commands, each in its own wrapper.
	wrapped, err := termimg.KittyTransmit(7, img, 42, 12, true)
	if err != nil {
		t.Fatalf("KittyTransmit through tmux: %v", err)
	}
	if got := strings.Count(wrapped, "\x1bPtmux;"); got != len(cmds) {
		t.Errorf("%d passthrough wrappers for %d commands", got, len(cmds))
	}
	if unwrap(t, wrapped) != raw {
		t.Errorf("the commands inside the tmux wrappers are not the direct ones")
	}
	tuitest.Golden(t, "kitty_transmit_tmux.golden", []byte(shape(wrapped, "\x1b\x1b\\\x1b\\", payloads)))
}

func TestKittyTransmitOneChunk(t *testing.T) {
	t.Parallel()
	img := chart(44, 24)
	raw, err := termimg.KittyTransmit(255, img, 5, 2, false)
	if err != nil {
		t.Fatalf("KittyTransmit: %v", err)
	}
	cmds := commands(t, raw)
	if len(cmds) != 1 || cmds[0].keys != "a=T,U=1,q=2,f=100,t=d,i=255,c=5,r=2,m=0" {
		t.Fatalf("commands = %d with keys %q, want one whose keys end m=0", len(cmds), cmds[0].keys)
	}
	file, err := base64.StdEncoding.DecodeString(cmds[0].payload)
	if err != nil {
		t.Fatalf("the payload is not base64: %v", err)
	}
	back, err := png.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("the payload is not a PNG: %v", err)
	}
	same(t, termimg.Scale(back, 44, 24), img)
}

func TestKittyTransmitRefuses(t *testing.T) {
	t.Parallel()
	img := chart(8, 8)
	cases := []struct {
		name           string
		id, cols, rows int
		img            *image.RGBA
	}{
		{name: "id zero means no image to kitty", id: 0, cols: 4, rows: 2, img: img},
		{name: "an id that does not fit a palette colour", id: 256, cols: 4, rows: 2, img: img},
		{name: "a negative id", id: -1, cols: 4, rows: 2, img: img},
		{name: "no columns", id: 1, cols: 0, rows: 2, img: img},
		{name: "no rows", id: 1, cols: 4, rows: 0, img: img},
		{name: "more columns than there are marks", id: 1, cols: 298, rows: 2, img: img},
		{name: "more rows than there are marks", id: 1, cols: 4, rows: 298, img: img},
		{name: "no picture", id: 1, cols: 4, rows: 2, img: nil},
		{name: "an empty picture", id: 1, cols: 4, rows: 2, img: image.NewRGBA(image.Rectangle{})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := termimg.KittyTransmit(c.id, c.img, c.cols, c.rows, false)
			if err == nil || raw != "" {
				t.Errorf("KittyTransmit = %q, %v; want nothing and an error", head(raw), err)
			}
		})
	}
	if _, err := termimg.KittyTransmit(1, img, 297, 297, true); err != nil {
		t.Errorf("the largest grid is refused: %v", err)
	}
}

func TestKittyPlaceholders(t *testing.T) {
	t.Parallel()
	rows := termimg.KittyPlaceholders(42, 3, 2)
	quoted := make([]string, len(rows))
	for i, r := range rows {
		quoted[i] = strconv.QuoteToASCII(r)
	}
	tuitest.Golden(t, "kitty_placeholders.golden", []byte(strings.Join(quoted, "\n")+"\n"))

	for _, grid := range [][3]int{{42, 3, 2}, {1, 44, 12}, {255, 42, 12}, {17, 1, 1}, {200, 297, 297}} {
		id, cols, n := grid[0], grid[1], grid[2]
		rows := termimg.KittyPlaceholders(id, cols, n)
		if len(rows) != n {
			t.Fatalf("grid %v: %d rows, want %d", grid, len(rows), n)
		}
		start, end := "\x1b[38;5;"+strconv.Itoa(id)+"m", "\x1b[39m"
		for y, row := range rows {
			if !strings.HasPrefix(row, start) || !strings.HasSuffix(row, end) {
				t.Fatalf("grid %v row %d does not carry the id as a palette colour and put it back: %q", grid, y, head(row))
			}
			body := strings.TrimSuffix(strings.TrimPrefix(row, start), end)
			if strings.ContainsRune(body, '\x1b') {
				t.Fatalf("grid %v row %d holds another escape sequence", grid, y)
			}
			if w, wc := ansi.StringWidth(row), ansi.StringWidthWc(row); w != cols || wc != cols {
				t.Fatalf("grid %v row %d is %d cells wide by graphemes and %d by wcwidth, want %d", grid, y, w, wc, cols)
			}
			cells := []rune(body)
			if len(cells) != 3*cols {
				t.Fatalf("grid %v row %d has %d characters, want three a cell", grid, y, len(cells))
			}
			for x := range cols {
				c := cells[3*x : 3*x+3]
				if c[0] != kitty.Placeholder || c[1] != kitty.Diacritic(y) || c[2] != kitty.Diacritic(x) {
					t.Fatalf("grid %v cell (%d,%d) = %U, want the placeholder marked with its row and column", grid, x, y, c)
				}
				one := string(c)
				if w, wc := ansi.StringWidth(one), ansi.StringWidthWc(one); w != 1 || wc != 1 {
					t.Fatalf("grid %v cell (%d,%d) is %d wide by graphemes and %d by wcwidth, want 1", grid, x, y, w, wc)
				}
			}
		}
	}
}

func TestKittyPlaceholdersRefuses(t *testing.T) {
	t.Parallel()
	for _, grid := range [][3]int{{0, 3, 2}, {256, 3, 2}, {-4, 3, 2}, {1, 0, 2}, {1, 3, 0}, {1, 298, 2}, {1, 3, 298}} {
		if got := termimg.KittyPlaceholders(grid[0], grid[1], grid[2]); got != nil {
			t.Errorf("KittyPlaceholders%v = %d rows, want nil", grid, len(got))
		}
	}
}

// TestDiacritics pins what the grid limit of 297 rests on: the library's
// table of row and column marks has that many, all different, and answers
// with the first one past its end.
func TestDiacritics(t *testing.T) {
	t.Parallel()
	seen := make(map[rune]int)
	for i := range 297 {
		d := kitty.Diacritic(i)
		if at, dup := seen[d]; dup {
			t.Fatalf("marks %d and %d are both %U", at, i, d)
		}
		seen[d] = i
	}
	if kitty.Diacritic(297) != kitty.Diacritic(0) {
		t.Errorf("mark 297 is %U: the table grew, and the limit can too", kitty.Diacritic(297))
	}
}

func TestKittyDelete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   int
		tmux bool
		want string
	}{
		{name: "direct", id: 7, want: "\x1b_Ga=d,d=I,i=7,q=2\x1b\\"},
		{name: "the largest id", id: 255, want: "\x1b_Ga=d,d=I,i=255,q=2\x1b\\"},
		{name: "through tmux", id: 7, tmux: true, want: "\x1bPtmux;\x1b\x1b_Ga=d,d=I,i=7,q=2\x1b\x1b\\\x1b\\"},
		{name: "id zero", id: 0, want: ""},
		{name: "id 256", id: 256, tmux: true, want: ""},
	}
	var golden strings.Builder
	for _, c := range cases {
		got := termimg.KittyDelete(c.id, c.tmux)
		golden.WriteString("# " + c.name + "\n" + strconv.Quote(got) + "\n")
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got != c.want {
				t.Errorf("KittyDelete(%d, %v) = %q, want %q", c.id, c.tmux, got, c.want)
			}
			if c.tmux && got != "" && unwrap(t, got) != termimg.KittyDelete(c.id, false) {
				t.Errorf("the command inside the tmux wrapper is not the direct one")
			}
		})
	}
	tuitest.Golden(t, "kitty_delete.golden", []byte(golden.String()))
}
