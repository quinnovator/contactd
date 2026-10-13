package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

const (
	// halfBlock is the cell a picture is made of where it is drawn with
	// characters, and placeholder the cell that shows a picture the terminal
	// was sent.
	halfBlock   = "▀"
	placeholder = "\U0010EEEE"
	// chartReply is a reply that names one picture between two lines.
	chartReply = "Here is the chart:\n\n![latency by day](latency.png)\n\nIt peaked on Thursday."
)

// chartPNG is a chart as a tool would save it: bars on a dark ground, w by h
// pixels, none of them clear.
func chartPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	ground := color.RGBA{R: 17, G: 20, B: 23, A: 255}
	bars := [2]color.RGBA{{R: 94, G: 156, B: 220, A: 255}, {R: 230, G: 150, B: 60, A: 255}}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c, bar := ground, x*14/w
			if tall := h * (3 + bar*5%7) / 10; y >= h-tall && x*28/w%2 == 0 {
				c = bars[bar%2]
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// sentPicture is a picture as the terminal was sent it: its id, the cells it
// is placed on, and the size of the PNG that travelled.
type sentPicture struct{ id, cols, rows, w, h int }

// kittyBytes reads bytes written for the terminal beside the screen. They
// are kitty graphics commands and nothing else: pictures sent, each a PNG in
// chunks, and the ids of pictures to forget.
func kittyBytes(t *testing.T, raw string) (sent []sentPicture, forgot []int) {
	t.Helper()
	var payload strings.Builder
	for raw != "" {
		// Through tmux each command is wrapped, with its escapes doubled.
		if wrapped, ok := strings.CutPrefix(raw, "\x1bPtmux;"); ok {
			inner, after, found := strings.Cut(wrapped, "\x1b\x1b\\\x1b\\")
			if !found {
				t.Fatalf("a tmux passthrough that does not end: %q", raw[:min(len(raw), 80)])
			}
			raw = strings.ReplaceAll(inner, "\x1b\x1b", "\x1b") + "\x1b\\" + after
		}
		rest, ok := strings.CutPrefix(raw, "\x1b_G")
		command, after, ended := strings.Cut(rest, "\x1b\\")
		if !ok || !ended {
			t.Fatalf("not a kitty graphics command: %q", raw[:min(len(raw), 80)])
		}
		head, data, _ := strings.Cut(command, ";")
		raw = after
		keys := map[string]string{}
		for kv := range strings.SplitSeq(head, ",") {
			k, v, _ := strings.Cut(kv, "=")
			keys[k] = v
		}
		num := func(k string) int {
			n, err := strconv.Atoi(keys[k])
			if err != nil {
				t.Fatalf("the command %q has no number for %s", head, k)
			}
			return n
		}
		if keys["q"] != "" && keys["q"] != "2" {
			t.Errorf("the command %q asks the terminal to answer", head)
		}
		switch keys["a"] {
		case "d":
			if keys["d"] != "I" || keys["q"] != "2" {
				t.Errorf("a delete is %q", head)
			}
			forgot = append(forgot, num("i"))
			continue
		case "T":
			if keys["U"] != "1" || keys["f"] != "100" || keys["t"] != "d" || keys["q"] != "2" {
				t.Errorf("a picture is sent with %q", head)
			}
			sent = append(sent, sentPicture{id: num("i"), cols: num("c"), rows: num("r")})
			payload.Reset()
		case "":
		default:
			t.Fatalf("the command %q is neither a picture nor a delete", head)
		}
		if len(sent) == 0 {
			t.Fatalf("a chunk %q before any picture", head)
		}
		payload.WriteString(data)
		if keys["m"] == "1" {
			continue
		}
		file, err := base64.StdEncoding.DecodeString(payload.String())
		if err != nil {
			t.Fatalf("the picture's payload: %v", err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(file))
		if err != nil {
			t.Fatalf("the picture is not a PNG: %v", err)
		}
		sent[len(sent)-1].w, sent[len(sent)-1].h = cfg.Width, cfg.Height
	}
	return sent, forgot
}

// served is the fake server with a real one's files: everything but File is
// the fake's, and a file is fetched over the API from a contact's directory.
type served struct {
	*fakeBackend

	remote *client.Client
}

func (s *served) File(ctx context.Context, id core.ContactID, path, etag string) (client.File, error) {
	defer s.fetching(id, path, etag)()
	f, err := s.remote.File(ctx, id, path, etag)
	if err != nil {
		return f, fmt.Errorf("the server: %w", err)
	}
	return f, nil
}

// imageRig is a harness whose contact has files. Every command Update
// answers with is kept and run by the test, and what was written to the
// terminal beside the screen is written down.
type imageRig struct {
	*harness

	ada client.Contact
	// put gives ada a file, last written at a time; remove takes one away.
	put    func(name string, data []byte, at time.Time)
	remove func(name string)
	// home is the person's home directory and work where the client was
	// started.
	home, work string
	// raws is every write to the terminal beside the screen, in order.
	raws []string
}

// newImageRig makes the interface for a terminal that can do caps, 100
// columns by 30 rows, over the fake server. Nothing is ever fetched while
// Update runs.
func newImageRig(t *testing.T, caps termcaps.Caps, opt Options) *imageRig {
	t.Helper()
	if opt.Env == (termcaps.Env{}) {
		opt.Env, opt.Overrides = terminal(t, caps)
	}
	home := t.TempDir()
	opt.Home, opt.WorkDir, opt.Dirs = home, filepath.Join(home, "work"), conf.Dirs{Config: filepath.Join(home, "contactd")}
	if err := os.Mkdir(opt.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ada := client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"}
	r := &imageRig{harness: newHarnessWith(t, opt, ada), ada: ada, home: home, work: opt.WorkDir}
	b := r.b
	b.files = map[string]client.File{}
	r.put = func(name string, data []byte, at time.Time) {
		h := fnv.New64a()
		_, _ = h.Write(data) // a hash's Write never fails
		b.files[name] = client.File{Data: data, ETag: fmt.Sprintf(`"%x"`, h.Sum64()), Modified: at}
	}
	r.remove = func(name string) { delete(b.files, name) }
	r.keep = true
	r.m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	r.around = func(in bool) {
		b.mu.Lock()
		b.loop = in
		b.mu.Unlock()
	}
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.onLoop) > 0 {
			t.Errorf("files were fetched from the update loop: %q", b.onLoop)
		}
	})
	r.size(100, 30)
	return r
}

// newServedRig is newImageRig over a real server's files: ada is a contact
// of a running API, and her files are in a directory it serves.
func newServedRig(t *testing.T, caps termcaps.Caps, opt Options) *imageRig {
	t.Helper()
	root := t.TempDir()
	srv := apitest.Start(t, apitest.Options{FSRoot: root})
	remote, err := client.New(client.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	id, err := remote.Create(t.Context(), client.NewContact{Agent: core.AgentConfig{System: "You are Ada."}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, id.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := newImageRig(t, caps, opt)
	r.ada.ID = id
	r.m.opt.Backend = &served{fakeBackend: r.b, remote: remote}
	r.put = func(name string, data []byte, at time.Time) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	r.remove = func(name string) {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// talk opens the conversation with ada, connected, with a page of the log: a
// question, and then these replies, numbered from 5. All of it was said an
// hour ago. The session has nothing more to say by itself.
func (r *imageRig) talk(replies ...string) *fakeSession {
	r.t.Helper()
	at := fixedNow().Add(-time.Hour)
	events := make([]core.Event, 0, 1+len(replies))
	events = append(events, logged(r.t, 1, at, core.KindUserMsg, core.MsgBody{Text: "plot last week's latency"}))
	for i, reply := range replies {
		events = append(events, logged(r.t, uint64(5+i), at, core.KindAgentMsg, core.MsgBody{Text: reply}))
	}
	return r.open(client.Update{Kind: client.UpdateEvents, Start: true, Events: events})
}

// open opens the conversation with ada, connected, with these updates.
func (r *imageRig) open(updates ...client.Update) *fakeSession {
	r.t.Helper()
	r.m.open(r.ada)
	sess := r.b.sessions[len(r.b.sessions)-1]
	close(sess.updates)
	r.from(sess, append([]client.Update{live()}, updates...)...)
	return sess
}

// started runs the commands kept so far and returns what they gave, not yet
// delivered. A write to the terminal is written down here.
func (r *imageRig) started() []tea.Msg {
	r.t.Helper()
	cmds := r.kept
	r.kept = nil
	var out []tea.Msg
	for _, cmd := range cmds {
		for _, msg := range inOrder(cmd) {
			if msg == nil {
				continue
			}
			if raw, ok := msg.(tea.RawMsg); ok {
				r.raws = append(r.raws, fmt.Sprint(raw.Msg))
			}
			out = append(out, msg)
		}
	}
	return out
}

// settle runs the commands kept so far and delivers what they give, and so
// on until nothing is on its way.
func (r *imageRig) settle() {
	r.t.Helper()
	for range 100 {
		msgs := r.started()
		if len(msgs) == 0 {
			return
		}
		for _, msg := range msgs {
			r.send(msg)
		}
	}
	r.t.Fatal("the commands never came to rest")
}

// does types a command, runs it, and waits for everything it started.
func (r *imageRig) does(line string) {
	r.t.Helper()
	r.command(line)
	r.settle()
}

// store is the open conversation's pictures.
func (r *imageRig) store() *termimg.Store { return r.m.chat.imgs.store }

// picture is the grid of the one picture on screen, made of cell: the rows
// that hold it and the cells in each. Every row of a picture is two cells in
// and has the same number of cells, and nothing else.
func (r *imageRig) picture(cell string) (cols, rows int) {
	r.t.Helper()
	strip := regexp.MustCompile(`[\x{0300}-\x{036F}\x{0483}-\x{0489}\x{0591}-\x{1AFF}\x{1DC0}-\x{1DFF}\x{20D0}-\x{20FF}\x{1D165}-\x{1D244}]`)
	for _, row := range r.rows() {
		if !strings.Contains(row, cell) {
			continue
		}
		// The marks that say which cell of the picture this is take no room.
		row = strings.TrimRight(strip.ReplaceAllString(row, ""), " ")
		n := strings.Count(row, cell)
		if row != "  "+strings.Repeat(cell, n) {
			r.t.Fatalf("a row of the picture is %q", row)
		}
		if cols != 0 && n != cols {
			r.t.Fatalf("a row of the picture has %d cells and another %d", n, cols)
		}
		cols, rows = n, rows+1
	}
	return cols, rows
}

// counts is the work done for pictures so far: what the store did, the
// writes to the terminal, and the files fetched.
type counts struct {
	jobs, encodes, raws, fetched int
}

func (r *imageRig) counts() counts {
	st := r.store().Stats()
	return counts{jobs: st.Jobs, encodes: st.Encodes, raws: len(r.raws), fetched: len(r.b.fetched())}
}

// asks is the paths that were fetched, each with the tag sent with it.
func (r *imageRig) asks() []string {
	asked := r.b.fetched()
	out := make([]string, 0, len(asked))
	for _, a := range asked {
		if a.id != r.ada.ID {
			r.t.Errorf("%s was asked of contact %d, and ada is %d", a.path, a.id, r.ada.ID)
		}
		out = append(out, strings.TrimSpace(a.path+" "+a.etag))
	}
	return out
}

func pixelCaps() termcaps.Caps {
	c := trueDark()
	c.Images = termcaps.ImagesPixels
	return c
}

func blockCaps() termcaps.Caps {
	c := trueDark()
	c.Images = termcaps.ImagesBlocks
	return c
}

// A picture in a reply is a line of text the moment the reply is drawn, and
// rows of half blocks once its file was fetched and decoded, in a command.
// The file comes over the API from the contact's directory on a real server.
func TestAnImageGoesFromItsLineToHalfBlocks(t *testing.T) {
	t.Parallel()
	r := newServedRig(t, blockCaps(), Options{})
	file := chartPNG(t, 640, 360)
	r.put("latency.png", file, fixedNow().Add(-2*time.Hour))
	r.talk(chartReply)

	// At once: the line, and a job that has not run.
	r.wantOnScreen("  Here is the chart:", "  [image: latency.png]", "  It peaked on Thursday.")
	r.wantNotOnScreen(halfBlock, "latency by day", "/image save")
	if got := r.counts(); got != (counts{jobs: 1}) {
		t.Fatalf("before the job ran: %+v", got)
	}

	r.settle()
	// The box at 100 by 30 is 44 by 10 cells, and a cell is 8 by 16 pixels:
	// ten rows of a 16:9 picture are 35 cells wide.
	if cols, rows := r.picture(halfBlock); cols != 35 || rows != 10 {
		t.Errorf("the picture is %d by %d cells, want 35 by 10:\n%s", cols, rows, r.screen())
	}
	caption := regexp.MustCompile(`(?m)^  latency\.png · 640×360 · \d+ kB · /image save 5 *$`)
	if !caption.MatchString(r.screen()) {
		t.Errorf("no caption under the picture:\n%s", r.screen())
	}
	r.wantOnScreen("  Here is the chart:", "  It peaked on Thursday.")
	r.wantNotOnScreen("[image:", "changed since", placeholder)
	if got, want := r.counts(), (counts{jobs: 1, encodes: 1, fetched: 1}); got != want {
		t.Errorf("after the job: %+v, want %+v", got, want)
	}
	if got := r.asks(); !slices.Equal(got, []string{"latency.png"}) {
		t.Errorf("fetched %q", got)
	}
	// The colours of the picture are on the screen, and no graphics command.
	if raw := r.m.View().Content; !strings.Contains(raw, "\x1b[38;2;94;156;220") || strings.Contains(raw, "\x1b_G") {
		t.Errorf("the picture's rows are drawn as %q", raw)
	}
	r.wantFits("a picture", 100, 30)

	// The conversation is opened again after the file was written anew: the
	// caption says that the file is newer than the reply that named it.
	r.put("latency.png", file, fixedNow())
	r.key(ctrl('p'))
	r.settle()
	r.talk(chartReply)
	r.settle()
	r.wantOnScreen(" · /image save 5 · changed since")
}

// At the pixel level the picture itself is sent to the terminal, once, in a
// write beside the screen, and the reply holds the cells that show it. The
// cells are drawn only after the picture was written.
func TestAtThePixelLevelThePictureIsSentOnce(t *testing.T) {
	t.Parallel()
	r := newServedRig(t, pixelCaps(), Options{})
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
	r.talk(chartReply)
	r.wantOnScreen("  [image: latency.png]")

	// The job has run and its result is in: the picture waits to be written,
	// and until it is the reply stays as it was.
	for _, msg := range r.started() {
		r.send(msg)
	}
	if len(r.raws) != 0 || len(r.kept) == 0 {
		t.Fatalf("after the job: %d writes, %d commands on their way", len(r.raws), len(r.kept))
	}
	r.wantOnScreen("  [image: latency.png]")
	r.wantNotOnScreen(placeholder)

	r.settle()
	if len(r.raws) != 1 {
		t.Fatalf("%d writes to the terminal, want 1", len(r.raws))
	}
	sent, forgot := kittyBytes(t, r.raws[0])
	if len(sent) != 1 || len(forgot) != 0 {
		t.Fatalf("the write holds %d pictures and %d deletes", len(sent), len(forgot))
	}
	// 35 by 10 cells of 8 by 16 pixels.
	if p := sent[0]; p.cols != 35 || p.rows != 10 || p.w != 280 || p.h != 160 {
		t.Errorf("sent %+v, want 35 by 10 cells and 280 by 160 pixels", p)
	}
	if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 {
		t.Errorf("the cells are %d by %d, want 35 by 10:\n%s", cols, rows, r.screen())
	}
	// The cells name the picture by its id, as a colour; no graphics command
	// is ever part of the screen.
	raw := r.m.View().Content
	if !strings.Contains(raw, fmt.Sprintf("\x1b[38;5;%dm", sent[0].id)) || strings.Contains(raw, "\x1b_G") || strings.Contains(raw, "\x1bP") {
		t.Errorf("the cells of picture %d are drawn as %q", sent[0].id, raw)
	}
	if !regexp.MustCompile(`(?m)^  latency\.png · 640×360 · \d+ kB · /image save 5 *$`).MatchString(r.screen()) {
		t.Errorf("no caption under the picture:\n%s", r.screen())
	}
	r.wantNotOnScreen("[image:", halfBlock)
	if got, want := r.counts(), (counts{jobs: 1, encodes: 1, raws: 1, fetched: 1}); got != want {
		t.Errorf("after the job: %+v, want %+v", got, want)
	}
	// No reply is left waiting to be drawn again.
	if im := r.m.chat.imgs; len(im.held) != 0 || len(im.going) != 0 || r.m.raw.busy {
		t.Errorf("replies still wait for a write: held %v, going %v, a write on its way: %v", im.held, im.going, r.m.raw.busy)
	}
	r.wantFits("a picture", 100, 30)
}

// What is written to the terminal beside the screen goes out one write at a
// time, in the order it was queued, and the program is asked to say when a
// write is done only after the write itself. What was queued meanwhile goes
// out together, as one write.
func TestWritesBesideTheScreenGoOutInOrder(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, pixelCaps(), Options{})
	m := r.m
	if m.flush() != nil || m.raw.used {
		t.Fatal("with nothing queued there is a write")
	}
	m.write("")
	if m.flush() != nil || m.raw.used {
		t.Fatal("no bytes are a write")
	}
	m.write("one")
	first := m.flush()()
	// The two steps are run one after the other, never together.
	if _, together := first.(tea.BatchMsg); together {
		t.Fatal("the write and the word that it is done are run together")
	}
	steps := inOrder(func() tea.Msg { return first })
	if !reflect.DeepEqual(steps, []tea.Msg{tea.RawMsg{Msg: "one"}, rawWrittenMsg{}}) {
		t.Fatalf("the write is %#v, want the bytes and then the word that they are written", steps)
	}
	if !m.raw.busy || !m.raw.used || len(m.raw.queue) != 0 {
		t.Fatalf("after the write was started: %+v", m.raw)
	}

	// Queued while that write is on its way: nothing goes out until it is
	// done, and then all of it, oldest first.
	m.write("two")
	m.write("three")
	if m.flush() != nil {
		t.Fatal("a second write was started before the first was done")
	}
	r.send(steps[0])
	if got := r.started(); len(got) != 0 {
		t.Fatalf("the bytes coming back through Update started %#v", got)
	}
	r.send(steps[1])
	if got := r.started(); !reflect.DeepEqual(got, []tea.Msg{tea.RawMsg{Msg: "twothree"}, rawWrittenMsg{}}) {
		t.Fatalf("after the first write the next is %#v", got)
	}
	if !m.raw.busy || len(m.raw.queue) != 0 {
		t.Fatalf("after the second write was started: %+v", m.raw)
	}
	r.send(rawWrittenMsg{})
	if got := r.started(); len(got) != 0 || m.raw.busy {
		t.Fatalf("with nothing left to write: %#v, a write on its way: %v", got, m.raw.busy)
	}
}

// The cells that show a picture are drawn only once the picture was written
// to the terminal, whatever has the reply drawn in between: another picture
// of the same reply that is ready, or a key that draws everything again.
// Until then the picture is the line it was while it loaded.
func TestAPictureIsALineUntilItIsWritten(t *testing.T) {
	t.Parallel()
	cells := regexp.MustCompile(`\x1b\[38;5;(\d+)m` + placeholder)
	// early is the pictures whose cells are on screen and which the terminal
	// was not sent.
	early := func(r *imageRig) []string {
		r.t.Helper()
		sent, _ := kittyBytes(r.t, strings.Join(r.raws, ""))
		var ids []string
		for _, m := range cells.FindAllStringSubmatch(r.m.View().Content, -1) {
			if !slices.ContainsFunc(sent, func(p sentPicture) bool { return strconv.Itoa(p.id) == m[1] }) {
				ids = append(ids, m[1])
			}
		}
		return slices.Compact(ids)
	}
	t.Run("another picture of the reply is written first", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.put("errors.png", chartPNG(t, 320, 180), fixedNow().Add(-2*time.Hour))
		r.size(100, 60)
		r.talk("![latency](latency.png)\n\n![errors](errors.png)")
		// The first job is done and its picture is being written. The second
		// job is done before that write is: its picture is queued behind it.
		for _, msg := range r.started() {
			r.send(msg)
		}
		var write []tea.Msg
		for _, msg := range r.started() {
			if _, ok := msg.(imageDoneMsg); ok {
				r.send(msg)
				continue
			}
			write = append(write, msg)
		}
		if len(r.raws) != 1 || len(r.m.raw.queue) != 1 || len(write) != 2 {
			t.Fatalf("%d writes started, %d queued, %d messages of the first write", len(r.raws), len(r.m.raw.queue), len(write))
		}
		// The first write is done, and the reply is drawn again for it.
		for _, msg := range write {
			r.send(msg)
		}
		if ids := early(r); len(ids) != 0 {
			t.Fatalf("the cells of pictures %v are drawn, and the terminal was not sent them:\n%s", ids, r.screen())
		}
		r.wantOnScreen("[image: latency.png]")
		r.settle()
		if ids := early(r); len(ids) != 0 || len(r.raws) != 2 {
			t.Fatalf("after %d writes the cells of pictures %v are drawn, and the terminal was not sent them", len(r.raws), ids)
		}
		if n := len(slices.Compact(cells.FindAllString(r.m.View().Content, -1))); n != 2 {
			t.Errorf("%d pictures are on screen, want 2:\n%s", n, r.screen())
		}
		r.wantNotOnScreen("[image:")
	})
	t.Run("a picture of another reply does not wait", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.put("errors.png", chartPNG(t, 320, 180), fixedNow().Add(-2*time.Hour))
		r.size(100, 60)
		r.talk("![latency](latency.png)", "![errors](errors.png)")
		// As above, with each picture in a reply of its own: once the first
		// write is done its picture is drawn, whatever waits behind it.
		for _, msg := range r.started() {
			r.send(msg)
		}
		var write []tea.Msg
		for _, msg := range r.started() {
			if _, ok := msg.(imageDoneMsg); ok {
				r.send(msg)
				continue
			}
			write = append(write, msg)
		}
		for _, msg := range write {
			r.send(msg)
		}
		if n := len(slices.Compact(cells.FindAllString(r.m.View().Content, -1))); n != 1 || len(early(r)) != 0 || len(r.raws) != 1 {
			t.Fatalf("after the first write %d pictures are on screen, %v of them early, %d writes:\n%s", n, early(r), len(r.raws), r.screen())
		}
		r.settle()
		if n := len(slices.Compact(cells.FindAllString(r.m.View().Content, -1))); n != 2 || len(early(r)) != 0 {
			t.Errorf("%d pictures are on screen, want 2:\n%s", n, r.screen())
		}
	})
	t.Run("a picture of the reply that is up stays up", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.put("errors.png", chartPNG(t, 320, 180), fixedNow().Add(-2*time.Hour))
		r.size(100, 60)
		r.talk("![latency](latency.png)\n\n![errors](errors.png)")
		// The first picture is written and drawn. Then the second job is
		// done, and before its picture is written everything is drawn again.
		for _, msg := range r.started() {
			r.send(msg)
		}
		var second tea.Msg
		for _, msg := range r.started() {
			if _, ok := msg.(imageDoneMsg); ok {
				second = msg
				continue
			}
			r.send(msg)
		}
		first := slices.Compact(cells.FindAllString(r.m.View().Content, -1))
		if len(first) != 1 || second == nil {
			t.Fatalf("after the first write %d pictures are on screen:\n%s", len(first), r.screen())
		}
		r.send(second)
		r.key(ctrl('o'))
		if now := slices.Compact(cells.FindAllString(r.m.View().Content, -1)); !slices.Equal(now, first) || len(early(r)) != 0 || len(r.raws) != 1 {
			t.Fatalf("with the second picture on its way the cells on screen are %q, were %q; %d writes:\n%s", now, first, len(r.raws), r.screen())
		}
		if n := strings.Count(r.screen(), "[image: "); n != 1 {
			t.Errorf("%d pictures are a line, want the one on its way:\n%s", n, r.screen())
		}
		r.settle()
		if n := len(slices.Compact(cells.FindAllString(r.m.View().Content, -1))); n != 2 || len(early(r)) != 0 {
			t.Errorf("%d pictures are on screen, want 2:\n%s", n, r.screen())
		}
	})
	t.Run("everything is drawn again", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.talk(chartReply)
		// The job is done; the write has not run.
		for _, msg := range r.started() {
			r.send(msg)
		}
		r.key(ctrl('o'))
		if ids := early(r); len(ids) != 0 || len(r.raws) != 0 {
			t.Fatalf("after %d writes the cells of pictures %v are drawn:\n%s", len(r.raws), ids, r.screen())
		}
		r.wantOnScreen("  [image: latency.png]")
		r.settle()
		if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 || len(r.raws) != 1 || len(early(r)) != 0 {
			t.Errorf("after %d writes the picture is %d by %d cells:\n%s", len(r.raws), cols, rows, r.screen())
		}
	})
}

// Once a picture is up, nothing but a new size of its box or a new level
// costs anything: scrolling, a resize that leaves the box as it was, the
// list of commands opening, a draft growing, the tool calls unfolding, a
// reply arriving and the clock all encode nothing, fetch nothing and write
// nothing to the terminal.
func TestWhatLeavesAPictureAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		caps termcaps.Caps
		cell string
		// smaller is what a smaller box costs.
		smaller counts
	}{
		{name: "half blocks", caps: blockCaps(), cell: halfBlock, smaller: counts{encodes: 1}},
		{name: "pixels", caps: pixelCaps(), cell: placeholder, smaller: counts{jobs: 1, encodes: 1, raws: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newServedRig(t, tc.caps, Options{})
			r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
			at := fixedNow().Add(-time.Hour)
			page := messages(t, 1, 40, at)
			page.Events = append(page.Events,
				logged(t, 41, at, core.KindAgentMsg, core.MsgBody{Text: chartReply}),
				logged(t, 42, at, core.KindUserMsg, core.MsgBody{Text: "thanks"}),
			)
			sess := r.open(page)
			r.settle()
			if cols, rows := r.picture(tc.cell); cols != 35 || rows != 10 {
				t.Fatalf("the picture is %d by %d cells:\n%s", cols, rows, r.screen())
			}
			was := r.counts()
			same := func(what string) {
				t.Helper()
				r.settle()
				if got := r.counts(); got != was {
					t.Errorf("%s: %+v, was %+v", what, got, was)
				}
			}
			shown := func(what string, cols, rows int) {
				t.Helper()
				if c, n := r.picture(tc.cell); c != cols || n != rows {
					t.Errorf("%s: the picture is %d by %d cells, want %d by %d:\n%s", what, c, n, cols, rows, r.screen())
				}
			}

			// Out of view and back.
			r.key(special(tea.KeyPgUp))
			r.key(special(tea.KeyPgUp))
			same("scrolled away")
			shown("scrolled away", 0, 0)
			r.key(special(tea.KeyPgDown))
			r.key(special(tea.KeyPgDown))
			same("scrolled back")
			shown("scrolled back", 35, 10)

			// A larger terminal whose box is the same 44 by 10.
			r.size(120, 32)
			same("a resize that keeps the box")
			shown("a resize that keeps the box", 35, 10)
			r.size(100, 30)
			same("the size it had")

			r.typeText("/")
			if !r.m.chat.pal.Open() {
				t.Fatal("the list of commands is not open")
			}
			same("the list of commands opening")
			r.key(ctrl('c'))
			r.typeText("one")
			r.key(ctrl('j'))
			r.typeText("two")
			same("a draft of two rows")
			r.key(ctrl('c'))

			r.key(ctrl('o'))
			same("unfolding")
			r.key(ctrl('o'))
			r.from(sess, typing(true), delta(core.Delta{Type: core.DeltaText, Text: "And "}))
			r.from(sess, delta(core.Delta{Type: core.DeltaMessage, Text: "And that is all.", Seq: 44}), typing(false))
			same("a reply arriving")
			// The clock's own next tick is not waited for.
			r.keep = false
			r.send(clockMsg{})
			r.keep = true
			r.send(tea.FocusMsg{})
			same("the clock")
			shown("after all that", 35, 10)

			// A shorter terminal has a smaller box, 44 by 8: the picture is
			// made again from what the client kept, and nothing is fetched.
			r.size(100, 24)
			r.settle()
			want := counts{
				jobs: was.jobs + tc.smaller.jobs, encodes: was.encodes + tc.smaller.encodes,
				raws: was.raws + tc.smaller.raws, fetched: was.fetched,
			}
			if got := r.counts(); got != want {
				t.Errorf("a smaller box: %+v, want %+v", got, want)
			}
			shown("a smaller box", 28, 8)
			if tc.smaller.raws == 1 {
				sent, forgot := kittyBytes(t, r.raws[len(r.raws)-1])
				first, _ := kittyBytes(t, r.raws[0])
				if len(sent) != 1 || len(forgot) != 0 || sent[0].id != first[0].id || sent[0].cols != 28 || sent[0].rows != 8 {
					t.Errorf("for the smaller box the terminal was sent %+v and told to forget %v; the first was %+v", sent, forgot, first)
				}
			}
		})
	}
}

// /image sets the level for the session. The picture is drawn again from
// what the client kept: nothing is fetched. Leaving the pixel level tells
// the terminal to forget the picture, and coming back sends it again.
func TestTheImageLevelIsChangedWithoutAFetch(t *testing.T) {
	t.Parallel()
	r := newServedRig(t, pixelCaps(), Options{})
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
	sess := r.talk(chartReply)
	r.settle()
	first, _ := kittyBytes(t, r.raws[0])
	id := first[0].id
	line := regexp.MustCompile(`(?m)^  \[image: latency\.png, 640×360, \d+ kB\] *\n  /image save 5 \./latency\.png *$`)

	for _, step := range []struct {
		command, note string
		// level is what the terminal can do after it. sent and forgot are the
		// pictures the terminal is then sent and told to forget.
		level        termcaps.Images
		sent, forgot int
		cell         string
	}{
		{
			command: "/image blocks", note: `image: blocks for this session; images = "blocks" in chat.toml keeps it`,
			level: termcaps.ImagesBlocks, forgot: 1, cell: halfBlock,
		},
		{
			command: "/image text", note: `image: text for this session; images = "text" in chat.toml keeps it`,
			level: termcaps.ImagesText,
		},
		{
			command: "/image PIXELS", note: `image: pixels for this session; images = "pixels" in chat.toml keeps it`,
			level: termcaps.ImagesPixels, sent: 1, cell: placeholder,
		},
		{
			// This terminal never said it has kitty graphics.
			command: "/image auto", note: `image: auto (blocks now) for this session; images = "auto" in chat.toml keeps it`,
			level: termcaps.ImagesBlocks, forgot: 1, cell: halfBlock,
		},
		{command: "/image pixels", note: `image: pixels for this session; images = "pixels" in chat.toml keeps it`, level: termcaps.ImagesPixels, sent: 1, cell: placeholder},
		{
			// Without the glyphs there are no cells to show a picture with.
			command: "/glyphs ascii", note: `glyphs: ascii for this session; glyphs = "ascii" in chat.toml keeps it`,
			level: termcaps.ImagesText, forgot: 1,
		},
	} {
		before := len(r.raws)
		cmd := r.command(step.command)
		if !slices.Contains(inOrder(cmd), tea.ClearScreen()) {
			t.Errorf("%s does not clear the screen", step.command)
		}
		if r.note() != step.note || r.m.chat.noteTone != toneDone {
			t.Errorf("%s says %q in the tone %q, want %q", step.command, r.note(), r.m.chat.noteTone, step.note)
		}
		r.settle()
		if r.m.caps.Images != step.level {
			t.Errorf("%s: the level is %v, want %v", step.command, r.m.caps.Images, step.level)
		}
		var sent []sentPicture
		var forgot []int
		for _, raw := range r.raws[before:] {
			s, f := kittyBytes(t, raw)
			sent, forgot = append(sent, s...), append(forgot, f...)
		}
		if len(sent) != step.sent || len(forgot) != step.forgot {
			t.Errorf("%s: the terminal was sent %+v and told to forget %v", step.command, sent, forgot)
		}
		// What is forgotten is the picture that was sent last, and a picture
		// sent again has an id of its own.
		for _, f := range forgot {
			if f != id {
				t.Errorf("%s: told to forget picture %d, want %d", step.command, f, id)
			}
		}
		for _, p := range sent {
			if p.id == id || p.cols != 35 || p.rows != 10 {
				t.Errorf("%s: sent %+v, want another picture than %d, on 35 by 10 cells", step.command, p, id)
			}
			id = p.id
		}
		switch {
		case step.cell != "":
			if cols, rows := r.picture(step.cell); cols != 35 || rows != 10 {
				t.Errorf("%s: the picture is %d by %d cells:\n%s", step.command, cols, rows, r.screen())
			}
			r.wantNotOnScreen("[image:")
		case r.m.caps.Glyphs == termcaps.GlyphsASCII:
			if s := r.screen(); !regexp.MustCompile(`(?m)^  \[image: latency\.png, 640x360, \d+ kB\] *\n  /image save 5 \./latency\.png *$`).MatchString(s) {
				t.Errorf("%s: no line for the picture:\n%s", step.command, s)
			}
		default:
			if s := r.screen(); !line.MatchString(s) {
				t.Errorf("%s: no line for the picture:\n%s", step.command, s)
			}
			r.wantNotOnScreen(halfBlock, placeholder)
		}
	}
	if got := r.asks(); !slices.Equal(got, []string{"latency.png"}) {
		t.Errorf("the file was fetched %q, want once", got)
	}

	// What is not a level changes nothing, and the line stays to be put right.
	for line, note := range map[string]string{
		"/image":            "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image sixel":      "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image text now":   "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save":       "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save five":  "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save 5.0":   "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save 0":     "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save 5 a b": "usage: /image pixels | blocks | text | auto | save <n> [path]",
		"/image save 9":     "no image 9 has been shown in this conversation",
		"/image save 5.2":   "no image 5.2 has been shown in this conversation",
	} {
		caps, writes := r.m.caps, len(r.raws)
		cmd := r.command(line)
		if cmd != nil || r.note() != note || r.draft() != line {
			t.Errorf("%s: gives %v, says %q and leaves %q in the box", line, cmd, r.note(), r.draft())
		}
		if r.m.caps != caps || len(r.raws) != writes {
			t.Errorf("%s changed what the terminal can do, or wrote to it", line)
		}
		r.key(ctrl('c'))
	}
	if len(sess.said) != 0 || sess.typings != 0 {
		t.Errorf("a command reached the contact: said %q, typing %d", sess.said, sess.typings)
	}
}

// A level the terminal cannot show gives the best it can, and the note says
// which that is.
func TestAnImageLevelTheTerminalCannotShow(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, light16(), Options{})
	r.talk("hello")
	r.does("/image pixels")
	if want := `image: pixels (blocks now) for this session; images = "pixels" in chat.toml keeps it`; r.note() != want {
		t.Errorf("the note is %q, want %q", r.note(), want)
	}
	if r.m.caps.Images != termcaps.ImagesBlocks || r.m.over.Images != "pixels" {
		t.Errorf("the level is %v and the setting %q", r.m.caps.Images, r.m.over.Images)
	}
}

// A file that is not there, one the server will not send, one that is no
// picture and one that cannot be decoded each show one line that says so.
// The files are read one at a time. /reload tries the failed ones again.
func TestAnImageThatCannotBeShown(t *testing.T) {
	t.Parallel()
	r := newServedRig(t, blockCaps(), Options{})
	file := chartPNG(t, 640, 360)
	at := fixedNow().Add(-2 * time.Hour)
	r.put("big.png", make([]byte, 8<<20+1), at)
	r.put("notes.png", []byte("p99 peaked on Thursday\n"), at)
	r.put("half.png", file[:len(file)/2], at)
	r.size(100, 60)
	r.talk("![a](missing.png)\n\n![b](big.png)\n\n![c](notes.png)\n\n![d](half.png)")
	r.wantOnScreen("  [image: missing.png]", "  [image: big.png]", "  [image: notes.png]", "  [image: half.png]")

	jobs := 0
	for range 20 {
		msgs := r.started()
		if len(msgs) == 0 {
			break
		}
		n := 0
		for _, msg := range msgs {
			if _, ok := msg.(imageDoneMsg); ok {
				n++
			}
			r.send(msg)
		}
		if n > 1 {
			t.Errorf("%d jobs ran at once", n)
		}
		jobs += n
	}
	if jobs != 4 || r.b.most != 1 {
		t.Errorf("%d jobs ran, at most %d files at once", jobs, r.b.most)
	}
	lines := []string{
		"  [image: missing.png, not found]",
		"  [image: big.png, too large]",
		"  [image: notes.png, not an image]",
		"  [image: half.png, could not be read; /reload to retry]",
	}
	r.wantOnScreen(lines...)
	r.wantNotOnScreen(halfBlock, "/image save")
	if got, want := r.counts(), (counts{jobs: 4, fetched: 4}); got != want {
		t.Errorf("after the jobs: %+v, want %+v", got, want)
	}

	// Two of the files are there now, and whole.
	r.put("missing.png", file, at)
	r.put("half.png", chartPNG(t, 320, 180), at)
	r.does("/reload")
	if !strings.HasPrefix(r.note(), "read chat.toml again") {
		t.Errorf("/reload says %q", r.note())
	}
	r.wantOnScreen(lines[1], lines[2])
	r.wantNotOnScreen("not found", "could not be read")
	captions := regexp.MustCompile(`(?m)^  missing\.png · 640×360 · \d+ kB · /image save 5 *$[\s\S]*^  half\.png · 320×180 · \d+ k?B · /image save 5\.4 *$`)
	if !captions.MatchString(r.screen()) {
		t.Errorf("the two pictures are not both drawn:\n%s", r.screen())
	}
	if got, want := r.counts(), (counts{jobs: 8, encodes: 2, fetched: 8}); got != want {
		t.Errorf("after /reload: %+v, want %+v", got, want)
	}
	r.wantFits("four pictures", 100, 60)
}

// /reload tries failed pictures again also where the client has no config
// directory to read anything from.
func TestReloadRetriesImagesWithNoConfigDirectory(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, blockCaps(), Options{})
	r.m.opt.Dirs = conf.Dirs{}
	r.talk(chartReply)
	r.settle()
	r.wantOnScreen("[image: latency.png, not found]")
	r.put("latency.png", chartPNG(t, 64, 36), fixedNow().Add(-2*time.Hour))
	r.does("/reload")
	if cols, rows := r.picture(halfBlock); cols == 0 || rows == 0 {
		t.Errorf("the picture is not drawn after /reload:\n%s", r.screen())
	}
}

// A file the server could not hand over this time is a line that says how
// to try again, whatever the reason was. Trying again asks whether the file
// is still the one the client holds, when it holds one.
func TestAFileThatCouldNotBeFetched(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, blockCaps(), Options{})
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
	r.b.failing = map[string]error{"latency.png": fmt.Errorf("get file: %w", client.ErrUnavailable)}
	r.talk(chartReply, "again:\n\n![the same](latency.png)")
	r.settle()
	if n := strings.Count(r.screen(), "[image: latency.png, could not be read; /reload to retry]"); n != 2 {
		t.Fatalf("%d lines say the file could not be read:\n%s", n, r.screen())
	}
	r.wantNotOnScreen(halfBlock, "/image save")

	r.b.failing = nil
	r.does("/reload")
	r.wantOnScreen("/image save 5", "/image save 6")
	r.wantNotOnScreen("could not be read")
	// The second reply's picture is the file the first one's job fetched.
	asked := r.asks()
	if len(asked) != 4 || asked[2] != "latency.png" || !strings.HasPrefix(asked[3], `latency.png "`) {
		t.Errorf("fetched %q, want the file once whole and once by its tag", asked)
	}
	if got := r.counts(); got.jobs != 4 || got.encodes != 2 {
		t.Errorf("the work done: %+v", got)
	}
}

// The setting the client was started with, from -images, CONTACTD_IMAGES or
// chat.toml, decides how a picture is drawn, whatever the terminal answers.
// Left alone, the level is the terminal's: half blocks until it says it has
// kitty graphics.
func TestTheImagesSettingChoosesTheLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		setting string
		level   termcaps.Images
		cell    string
	}{
		{setting: "", level: termcaps.ImagesBlocks, cell: halfBlock},
		{setting: "text", level: termcaps.ImagesText},
		{setting: "blocks", level: termcaps.ImagesBlocks, cell: halfBlock},
		{setting: "pixels", level: termcaps.ImagesPixels, cell: placeholder},
	} {
		t.Run("images="+tc.setting, func(t *testing.T) {
			t.Parallel()
			r := newImageRig(t, termcaps.Caps{}, Options{Env: asked(), Overrides: termcaps.Overrides{Images: tc.setting}})
			r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
			r.talk(chartReply)
			r.settle()
			if r.m.caps.Images != tc.level {
				t.Fatalf("the level is %v, want %v", r.m.caps.Images, tc.level)
			}
			if tc.cell == "" {
				r.wantOnScreen("[image: latency.png, 640×360, ", "/image save 5 ./latency.png")
				r.wantNotOnScreen(halfBlock, placeholder)
				return
			}
			if cols, rows := r.picture(tc.cell); cols != 35 || rows != 10 {
				t.Errorf("the picture is %d by %d cells:\n%s", cols, rows, r.screen())
			}
			// A terminal that then says it has kitty graphics changes only
			// what was left to it.
			r.send(kittyAnswer(31, "OK"))
			r.send(version("ghostty 1.2.0"))
			r.settle()
			want := tc.level
			if tc.setting == "" {
				want = termcaps.ImagesPixels
			}
			if r.m.caps.Images != want {
				t.Errorf("after the terminal's answers the level is %v, want %v", r.m.caps.Images, want)
			}
		})
	}
}

// There are 255 picture ids. When more pictures than that have been shown,
// a new one takes the id of the one shown longest ago, and the cells drawn
// for that one earlier would now show the new picture. Its reply is drawn
// again instead: every picture on screen is the one its reply names.
func TestMorePicturesThanIds(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, pixelCaps(), Options{})
	// Seven files, each as many cells wide as its number says, and one high.
	for k := range 7 {
		r.put(fmt.Sprintf("p%d.png", k), chartPNG(t, 8*(k+1), 16), fixedNow().Add(-2*time.Hour))
	}
	const replies = 300
	at := fixedNow().Add(-time.Hour)
	events := make([]core.Event, 0, replies)
	for i := range replies {
		events = append(events, logged(t, uint64(i+1), at, core.KindAgentMsg, core.MsgBody{Text: fmt.Sprintf("![p](p%d.png)", i%7)}))
	}
	r.open(client.Update{Kind: client.UpdateEvents, Start: true, Events: events})
	r.settle()

	// current is the grid of the picture the terminal holds under each id.
	current := func() map[int]sentPicture {
		t.Helper()
		held := map[int]sentPicture{}
		for _, raw := range r.raws {
			sent, forgot := kittyBytes(t, raw)
			for _, id := range forgot {
				delete(held, id)
			}
			for _, p := range sent {
				held[p.id] = p
			}
		}
		return held
	}
	cellsOf := regexp.MustCompile(`\x1b\[38;5;(\d+)m((?:` + placeholder + `\p{Mn}*)+)`)
	// check looks at every picture on screen: the cells name an id, the
	// terminal holds a picture under it, and that picture is as wide as the
	// file named in the caption under the cells.
	check := func(what string) int {
		t.Helper()
		held, rows, seen := current(), strings.Split(r.m.View().Content, "\n"), 0
		for i, row := range rows {
			m := cellsOf.FindStringSubmatch(row)
			if m == nil {
				continue
			}
			id, _ := strconv.Atoi(m[1])
			cells := strings.Count(m[2], placeholder)
			var k int
			if _, err := fmt.Sscanf(strings.TrimSpace(ansi.Strip(rows[i+1])), "p%d.png", &k); err != nil {
				continue // the caption is under the last row in view
			}
			if p, ok := held[id]; !ok || p.cols != k+1 || cells != k+1 {
				t.Errorf("%s: the cells of p%d.png are %d wide and name picture %d, which the terminal holds as %+v (%v)", what, k, cells, id, p, ok)
			}
			seen++
		}
		return seen
	}
	if check("at the newest") == 0 {
		t.Fatalf("no picture on screen:\n%s", r.screen())
	}
	// Up through all of them, a page at a time, and down again.
	for _, key := range []rune{tea.KeyPgUp, tea.KeyPgDown} {
		for page := range 60 {
			r.key(special(key))
			r.settle()
			if check(fmt.Sprintf("page %d", page)) == 0 {
				t.Fatalf("page %d: no picture on screen:\n%s", page, r.screen())
			}
		}
	}
	if got := r.store().Stats(); got.Jobs <= replies {
		t.Errorf("%d jobs for %d pictures: none was sent a second time", got.Jobs, replies)
	}
}

// "/image save" fetches the file again and writes its own bytes where the
// client was started, or where the path says: a new file for its owner
// alone, and never over one that is there.
func TestImageSave(t *testing.T) {
	t.Parallel()
	r := newServedRig(t, trueDark(), Options{})
	first, second := chartPNG(t, 640, 360), chartPNG(t, 64, 36)
	at := fixedNow().Add(-2 * time.Hour)
	r.put("latency.png", first, at)
	r.put("out/by day.png", second, at)
	r.put(".envrc", []byte("export TOKEN=1\n"), at)
	sess := r.talk("![a](latency.png)\n\nand\n\n![b](<out/by day.png>)", "![c](.envrc)")
	r.settle()
	r.wantOnScreen("/image save 5 ./latency.png", `/image save 5.2 "./by day.png"`, "[image: .envrc, not an image]")
	fetched := len(r.asks())

	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has the mode %v, want 0600", path, info.Mode().Perm())
		}
		return data
	}
	saved := func(line, note, path string, want []byte) {
		t.Helper()
		r.does(line)
		if r.note() != note || r.m.chat.noteTone != toneDone || r.draft() != "" {
			t.Errorf("%s says %q (tone %q) and leaves %q in the box, want %q", line, r.note(), r.m.chat.noteTone, r.draft(), note)
		}
		if got := read(path); !bytes.Equal(got, want) {
			t.Errorf("%s wrote %d bytes that are not the file's %d", line, len(got), len(want))
		}
	}
	refused := func(line, note string) {
		t.Helper()
		r.does(line)
		if r.note() != note || r.m.chat.noteTone != "" || r.draft() != line {
			t.Errorf("%s says %q (tone %q) and leaves %q in the box, want %q", line, r.note(), r.m.chat.noteTone, r.draft(), note)
		}
		r.key(ctrl('c'))
	}

	saved("/image save 5", fmt.Sprintf("saved latency.png to ./latency.png, %d bytes", len(first)), filepath.Join(r.work, "latency.png"), first)
	// The file is fetched for the save, whole, whatever the client holds.
	if got := r.asks(); len(got) != fetched+1 || got[fetched] != "latency.png" {
		t.Errorf("the save fetched %q", got[fetched:])
	}

	// The file on the server changes; the copy that is there is left alone.
	r.put("latency.png", second, at)
	refused("/image save 5", "not saved: ./latency.png is already there, and is not written over")
	if got := read(filepath.Join(r.work, "latency.png")); !bytes.Equal(got, first) {
		t.Error("a save wrote over a file")
	}
	// A path names the copy: under where the client started, or the home.
	saved("/image save 5 copy.png", fmt.Sprintf("saved latency.png to ./copy.png, %d bytes", len(second)), filepath.Join(r.work, "copy.png"), second)
	saved("/image save 5.2", fmt.Sprintf("saved by day.png to ./by day.png, %d bytes", len(second)), filepath.Join(r.work, "by day.png"), second)
	refused("/image save 5 ~/pics/", "not saved: ~/pics/latency.png: no such file or directory")
	if err := os.Mkdir(filepath.Join(r.home, "pics"), 0o700); err != nil {
		t.Fatal(err)
	}
	saved("/image save 5 ~/pics/", fmt.Sprintf("saved latency.png to ~/pics/latency.png, %d bytes", len(second)), filepath.Join(r.home, "pics", "latency.png"), second)
	// The word is taken in any case, as the levels are.
	saved("/image SAVE 5 loud.png", fmt.Sprintf("saved latency.png to ./loud.png, %d bytes", len(second)), filepath.Join(r.work, "loud.png"), second)
	// A directory takes the file under its own name.
	saved(`/image save 5.2 ~/pics`, fmt.Sprintf("saved by day.png to ~/pics/by day.png, %d bytes", len(second)), filepath.Join(r.home, "pics", "by day.png"), second)
	refused("/image save 5 ~/pics", "not saved: ~/pics/latency.png is already there, and is not written over")
	// A link is a file that is there, wherever it leads.
	if err := os.Symlink(filepath.Join(r.home, "elsewhere"), filepath.Join(r.work, "link.png")); err != nil {
		t.Fatal(err)
	}
	refused("/image save 5 link.png", "not saved: ./link.png is already there, and is not written over")
	if _, err := os.Lstat(filepath.Join(r.home, "elsewhere")); !os.IsNotExist(err) {
		t.Errorf("a save wrote through a link: %v", err)
	}

	// A name a reply chose never makes a hidden file by itself.
	refused("/image save 6", "not saved: .envrc would be a hidden file; name the file to save it as")
	refused("/image save 6 ~/pics", "not saved: .envrc would be a hidden file; name the file to save it as")
	saved("/image save 6 env.txt", "saved .envrc to ./env.txt, 15 bytes", filepath.Join(r.work, "env.txt"), []byte("export TOKEN=1\n"))

	// The file is gone from the contact's directory.
	r.remove("latency.png")
	refused("/image save 5 gone.png", "not saved: the contact has no file latency.png any more")
	if _, err := os.Lstat(filepath.Join(r.work, "gone.png")); !os.IsNotExist(err) {
		t.Errorf("a save that fetched nothing left a file: %v", err)
	}

	names := []string{}
	err := filepath.WalkDir(r.home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(r.home, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pics/by day.png", "pics/latency.png",
		"work/by day.png", "work/copy.png", "work/env.txt", "work/latency.png", "work/link.png", "work/loud.png",
	}
	if !slices.Equal(names, want) {
		t.Errorf("the saves left %q, want %q", names, want)
	}
	if len(sess.said) != 0 || len(r.raws) != 0 {
		t.Errorf("a save said %q to the contact and wrote %d times to the terminal", sess.said, len(r.raws))
	}
}

// A save that fetched nothing writes nothing, and says why in words of its
// own for a file the server will not send.
func TestASaveThatFetchesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	down := fmt.Errorf("get file: %w", client.ErrUnavailable)
	for _, tc := range []struct {
		name string
		err  error
		note string
	}{
		{name: "too large", err: fmt.Errorf("get file: %w", client.ErrFileTooLarge), note: "not saved: latency.png is larger than the server sends"},
		{name: "no server", err: down, note: "not saved: " + explain(down)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newImageRig(t, blockCaps(), Options{})
			r.put("latency.png", chartPNG(t, 64, 36), fixedNow().Add(-2*time.Hour))
			r.talk(chartReply)
			r.settle()
			r.b.failing = map[string]error{"latency.png": tc.err}
			const line = "/image save 5 copy.png"
			r.does(line)
			if r.note() != tc.note || r.m.chat.noteTone != "" || r.draft() != line {
				t.Errorf("the save says %q (tone %q) and leaves %q in the box, want %q", r.note(), r.m.chat.noteTone, r.draft(), tc.note)
			}
			left, err := os.ReadDir(r.work)
			if err != nil || len(left) != 0 {
				t.Errorf("the save left %v (%v), want nothing", left, err)
			}
		})
	}
}

// The person asked for the file, so a save goes on when its conversation is
// left meanwhile, and they are told where they are by then. A save that then
// fails puts its line back only into the box of the conversation that asked.
func TestASaveOutlivesItsConversation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// fails says the server cannot be reached when the save fetches.
		fails bool
		// again says the same contact is opened again before the save ends.
		again bool
	}{
		{name: "saved, on the list"},
		{name: "saved, in the conversation opened again", again: true},
		{name: "failed, on the list", fails: true},
		{name: "failed, in the conversation opened again", fails: true, again: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newImageRig(t, trueDark(), Options{})
			file := chartPNG(t, 64, 36)
			r.put("latency.png", file, fixedNow().Add(-2*time.Hour))
			r.talk(chartReply)
			r.settle()
			if tc.fails {
				r.b.failing = map[string]error{"latency.png": fmt.Errorf("get file: %w", client.ErrUnavailable)}
			}
			r.b.hold, r.b.waiting = make(chan struct{}), make(chan struct{}, 1)
			r.command("/image save 5")
			// The save runs, and waits for the server.
			done := make(chan []tea.Msg, 1)
			cmds := r.kept
			r.kept = nil
			go func() {
				msgs := make([]tea.Msg, 0, len(cmds))
				for _, cmd := range cmds {
					msgs = append(msgs, inOrder(cmd)...)
				}
				done <- msgs
			}()
			<-r.b.waiting
			r.key(ctrl('p'))
			if tc.again {
				r.talk(chartReply)
			}
			// Neither the list nor the picture is waited for: the server
			// answers nothing but the save.
			r.kept = nil
			close(r.b.hold)
			for _, msg := range <-done {
				if msg != nil {
					r.send(msg)
				}
			}

			note := r.m.note
			if tc.again {
				note = r.note()
				if r.draft() != "" {
					t.Errorf("the box of another conversation holds %q", r.draft())
				}
			}
			path := filepath.Join(r.work, "latency.png")
			if tc.fails {
				if !strings.HasPrefix(note, "not saved: ") {
					t.Errorf("the note is %q", note)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("a save that failed left a file: %v", err)
				}
				return
			}
			if want := fmt.Sprintf("saved latency.png to ./latency.png, %d bytes", len(file)); note != want {
				t.Errorf("the note is %q, want %q", note, want)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, file) {
				t.Errorf("the file was not written whole: %d bytes, %v", len(got), err)
			}
		})
	}
}

// Leaving a conversation tells the terminal to forget the pictures it was
// sent for it, whichever way it is left. Where no picture was sent, nothing
// is written.
func TestLeavingAConversationForgetsItsPictures(t *testing.T) {
	t.Parallel()
	bob := client.Contact{ID: 2, Name: "bob", Provider: "fake", Model: "fake-1"}
	for _, tc := range []struct {
		name  string
		leave func(r *imageRig)
		// to is the screen the person is on afterwards.
		to screen
	}{
		{name: "ctrl+p", leave: func(r *imageRig) { r.key(ctrl('p')) }, to: screenContacts},
		{name: "/contacts", leave: func(r *imageRig) { r.command("/contacts") }, to: screenContacts},
		{name: "/new", leave: func(r *imageRig) { r.command("/new") }, to: screenNew},
		{name: "/open", leave: func(r *imageRig) {
			r.command("/open bob")
			// The server names bob, and his conversation opens. It has
			// nothing to say.
			for _, msg := range r.started() {
				r.send(msg)
			}
			close(r.b.sessions[len(r.b.sessions)-1].updates)
		}, to: screenChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, caps := range []termcaps.Caps{pixelCaps(), blockCaps()} {
				r := newImageRig(t, caps, Options{})
				r.b.contacts = append(r.b.contacts, bob)
				r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
				r.talk(chartReply)
				r.settle()
				writes := len(r.raws)
				tc.leave(r)
				r.settle()
				if r.m.screen != tc.to || !r.b.sessions[0].closed {
					t.Fatalf("after leaving the screen is %v and the session closed is %v", r.m.screen, r.b.sessions[0].closed)
				}
				if caps.Images != termcaps.ImagesPixels {
					if len(r.raws) != 0 {
						t.Errorf("with half blocks %d writes went to the terminal", len(r.raws))
					}
					continue
				}
				if writes != 1 || len(r.raws) != 2 {
					t.Fatalf("%d writes before leaving and %d after, want 1 and 2", writes, len(r.raws))
				}
				sent, _ := kittyBytes(t, r.raws[0])
				again, forgot := kittyBytes(t, r.raws[1])
				if len(again) != 0 || len(forgot) != 1 || forgot[0] != sent[0].id {
					t.Errorf("on leaving the terminal was sent %+v and told to forget %v, want only picture %d forgotten", again, forgot, sent[0].id)
				}
			}
		})
	}
}

// A picture that is still on its way to the terminal when the conversation
// is left is forgotten after it was sent, never before. A job that is still
// running is stopped, and what it made is dropped.
func TestLeavingWhileAPictureIsOnItsWay(t *testing.T) {
	t.Parallel()
	t.Run("being written", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.talk(chartReply)
		// The job is done and its picture queued; the write has not run.
		for _, msg := range r.started() {
			r.send(msg)
		}
		r.key(ctrl('p'))
		r.settle()
		if len(r.raws) != 2 {
			t.Fatalf("%d writes to the terminal, want the picture and then the delete", len(r.raws))
		}
		sent, none := kittyBytes(t, r.raws[0])
		nothing, forgot := kittyBytes(t, r.raws[1])
		if len(sent) != 1 || len(none) != 0 || len(nothing) != 0 || len(forgot) != 1 || forgot[0] != sent[0].id {
			t.Errorf("the writes were %+v/%v and then %+v/%v", sent, none, nothing, forgot)
		}
		if r.m.raw.busy || len(r.m.raw.queue) != 0 {
			t.Errorf("writes are still on their way: %+v", r.m.raw)
		}
	})
	t.Run("queued behind another", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.put("errors.png", chartPNG(t, 320, 180), fixedNow().Add(-2*time.Hour))
		r.talk(chartReply, "![errors](errors.png)")
		// The first job is done and its picture is being written; the second
		// job has run. Its picture comes in before that write is done, and
		// then the conversation is left.
		for _, msg := range r.started() {
			r.send(msg)
		}
		var write []tea.Msg
		for _, msg := range r.started() {
			if _, ok := msg.(imageDoneMsg); ok {
				r.send(msg)
				continue
			}
			write = append(write, msg)
		}
		if len(r.raws) != 1 || !r.m.raw.busy || len(r.m.raw.queue) != 1 {
			t.Fatalf("%d writes started, one on its way: %v, %d queued", len(r.raws), r.m.raw.busy, len(r.m.raw.queue))
		}
		r.key(ctrl('p'))
		if len(r.m.raw.queue) != 2 {
			t.Fatalf("%d writes are queued, want the second picture and the deletes", len(r.m.raw.queue))
		}
		for _, msg := range write {
			r.send(msg)
		}
		r.settle()
		if len(r.raws) != 2 {
			t.Fatalf("%d writes to the terminal, want two", len(r.raws))
		}
		first, none := kittyBytes(t, r.raws[0])
		second, forgot := kittyBytes(t, r.raws[1])
		if len(first) != 1 || len(none) != 0 || len(second) != 1 || len(forgot) != 2 {
			t.Fatalf("the writes were %+v/%v and then %+v/%v", first, none, second, forgot)
		}
		slices.Sort(forgot)
		want := []int{first[0].id, second[0].id}
		slices.Sort(want)
		if !slices.Equal(forgot, want) {
			t.Errorf("told to forget %v, want %v", forgot, want)
		}
		// The second picture is sent before anything is forgotten.
		if sent, del := strings.Index(r.raws[1], "\x1b_Ga=T"), strings.Index(r.raws[1], "\x1b_Ga=d"); sent != 0 || del < sent {
			t.Errorf("in the last write the picture is at byte %d and the first delete at %d", sent, del)
		}
		if r.m.raw.busy || len(r.m.raw.queue) != 0 {
			t.Errorf("writes are still on their way: %+v", r.m.raw)
		}
	})
	t.Run("being fetched", func(t *testing.T) {
		t.Parallel()
		r := newImageRig(t, pixelCaps(), Options{})
		r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
		r.b.hold, r.b.waiting = make(chan struct{}), make(chan struct{}, 1)
		r.talk(chartReply)
		// The job runs, and waits for the server.
		done := make(chan []tea.Msg, 1)
		cmds := r.kept
		r.kept = nil
		go func() {
			msgs := make([]tea.Msg, 0, len(cmds))
			for _, cmd := range cmds {
				msgs = append(msgs, inOrder(cmd)...)
			}
			done <- msgs
		}()
		// Leaving ends the fetch: nothing else would, the server never answers.
		<-r.b.waiting
		r.key(ctrl('p'))
		r.b.mu.Lock()
		gaveUp := r.b.gaveUp
		r.b.mu.Unlock()
		if gaveUp() == nil {
			close(r.b.hold)
			<-done
			t.Fatal("leaving the conversation did not stop the fetch of its picture")
		}
		msgs := <-done
		jobs := 0
		for _, msg := range msgs {
			if _, ok := msg.(imageDoneMsg); ok {
				jobs++
			}
			if msg != nil {
				r.send(msg)
			}
		}
		r.settle()
		if jobs != 1 || len(r.raws) != 0 || r.m.chat != nil {
			t.Errorf("%d jobs ended, %d writes went to the terminal, the conversation is %v", jobs, len(r.raws), r.m.chat)
		}
		// The same contact opened again starts from nothing, and is not
		// handed what the old job made.
		close(r.b.hold)
		r.b.waiting = nil
		r.talk(chartReply)
		r.settle()
		if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 || len(r.raws) != 1 {
			t.Errorf("opened again: the picture is %d by %d cells after %d writes:\n%s", cols, rows, len(r.raws), r.screen())
		}
	})
}

// A reply can only name a file in its contact's own directory. Whatever
// else it names is text: nothing is fetched for it, from the server or from
// anywhere else.
func TestAReplyCannotMakeTheClientFetchFromElsewhere(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dest string
		// asked is the one file that is asked for, of the contact's own.
		asked string
	}{
		{name: "a web address", dest: "https://evil.example/p.png?d=secret"},
		{name: "a host with no scheme", dest: "//evil.example/p.png"},
		{name: "a path that climbs out", dest: "../x.png"},
		{name: "a path that climbs out further in", dest: "charts/../../x.png"},
		{name: "an encoded climb", dest: "%2e%2e/x.png"},
		{name: "a file address", dest: "file:///etc/passwd"},
		{name: "data", dest: "data:image/png;base64,iVBORw0KGgo="},
		{name: "a drive", dest: `C:\\Users\\x.png`},
		// The contact's directory is the root a reply sees: a leading slash
		// names a file in it, and never one of the person's own.
		{name: "a path from the root", dest: "/etc/passwd", asked: "etc/passwd"},
		{name: "a file of the contact's", dest: "out/latency.png", asked: "out/latency.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, caps := range []termcaps.Caps{pixelCaps(), blockCaps(), trueDark()} {
				r := newImageRig(t, caps, Options{})
				r.talk("![a chart]("+tc.dest+")", "see ![a chart]("+tc.dest+") here")
				r.settle()
				var want []string
				if tc.asked != "" {
					// Once for each of the two replies that name it.
					want = []string{tc.asked, tc.asked}
				}
				if got := r.asks(); !slices.Equal(got, want) {
					t.Errorf("fetched %q, want %q", got, want)
				}
				if len(r.raws) != 0 {
					t.Errorf("%d writes went to the terminal", len(r.raws))
				}
				if tc.asked == "" {
					r.wantNotOnScreen("[image:", "/image save")
				} else {
					r.wantOnScreen("[image: " + tc.asked[strings.LastIndex(tc.asked, "/")+1:] + ", not found]")
				}
			}
		})
	}
}

// The pictures are kept by the sequence number of their reply. An item
// without one, which the transcript never draws as a reply, would share its
// place with every other such item: it is answered with nothing, and no file
// is wanted for it.
func TestAnItemWithNoNumberHasNoPicture(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, blockCaps(), Options{})
	r.put("a.png", chartPNG(t, 64, 36), fixedNow())
	r.talk("hello")
	r.settle()
	im := r.m.chat.imgs
	if got := im.Image(draw.ImageRef{N: 1, Path: "a.png"}, 44, 10); !reflect.DeepEqual(got, draw.Shown{}) {
		t.Errorf("an item with no number is shown as %+v", got)
	}
	if _, ok := im.store.Next(); ok {
		t.Error("a file is wanted for an item with no number")
	}
	// With a number it is a line at once, and a job.
	if got := im.Image(draw.ImageRef{Seq: 9, N: 1, Path: "a.png"}, 44, 10); got.Line != "[image: a.png]" {
		t.Errorf("an item with a number is shown as %+v", got)
	}
	if _, ok := im.store.Next(); !ok {
		t.Error("no file is wanted for an item with a number")
	}
}

// The first picture id is taken from the clock, so that two clients in one
// terminal seldom start at the same one.
func TestPictureIdsStartAtTheClock(t *testing.T) {
	t.Parallel()
	ids := map[int]bool{}
	for _, later := range []time.Duration{0, time.Millisecond, 7 * time.Millisecond, 254 * time.Millisecond} {
		r := newImageRig(t, pixelCaps(), Options{})
		r.now = fixedNow().Add(later)
		r.put("latency.png", chartPNG(t, 64, 36), fixedNow().Add(-2*time.Hour))
		r.talk(chartReply)
		r.settle()
		sent, _ := kittyBytes(t, strings.Join(r.raws, ""))
		if len(sent) != 1 || sent[0].id < 1 || sent[0].id > 255 {
			t.Fatalf("%v later the terminal was sent %+v", later, sent)
		}
		ids[sent[0].id] = true
	}
	if len(ids) != 4 {
		t.Errorf("four clients started a millisecond or more apart use the ids %v", ids)
	}
}

// Inside tmux the picture and the word to forget it travel in tmux's
// passthrough, each command in its own.
func TestPicturesGoThroughTmux(t *testing.T) {
	t.Parallel()
	env := termcaps.Env{Term: "tmux-256color", Tmux: "/tmp/tmux-501/default,1,0", Detected: termcaps.ColorTrue}
	over := termcaps.Overrides{Glyphs: "unicode", Theme: "dark", Images: "pixels", NoProbe: true}
	r := newImageRig(t, termcaps.Caps{}, Options{Env: env, Overrides: over})
	if !r.m.caps.Tmux || r.m.caps.Images != termcaps.ImagesPixels {
		t.Fatalf("the terminal can do %+v", r.m.caps)
	}
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
	r.talk(chartReply)
	r.settle()
	r.key(ctrl('p'))
	r.settle()
	if len(r.raws) != 2 {
		t.Fatalf("%d writes to the terminal", len(r.raws))
	}
	for _, raw := range r.raws {
		if !strings.HasPrefix(raw, "\x1bPtmux;\x1b\x1b_G") || !strings.HasSuffix(raw, "\x1b\x1b\\\x1b\\") {
			t.Errorf("a write is not in tmux's passthrough: %q…%q", raw[:min(len(raw), 40)], raw[max(len(raw)-12, 0):])
		}
	}
	sent, _ := kittyBytes(t, r.raws[0])
	_, forgot := kittyBytes(t, r.raws[1])
	if len(sent) != 1 || len(forgot) != 1 || forgot[0] != sent[0].id {
		t.Errorf("sent %+v, forgot %v", sent, forgot)
	}
}

// What the terminal answers after the start reaches the pictures that are
// already drawn: half blocks become the picture itself once the terminal
// says it has kitty graphics, at the size of the cells it reports.
func TestALateAnswerTurnsHalfBlocksIntoPixels(t *testing.T) {
	t.Parallel()
	r := newImageRig(t, termcaps.Caps{}, Options{Env: asked()})
	r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
	r.talk(chartReply)
	r.settle()
	if cols, rows := r.picture(halfBlock); cols != 35 || rows != 10 || len(r.raws) != 0 {
		t.Fatalf("before the answers: %d by %d cells, %d writes:\n%s", cols, rows, len(r.raws), r.screen())
	}

	r.send(kittyAnswer(31, "OK"))
	r.send(version("ghostty 1.2.0"))
	r.settle()
	if r.m.caps.Images != termcaps.ImagesPixels {
		t.Fatalf("the level is %v", r.m.caps.Images)
	}
	if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 || len(r.raws) != 1 {
		t.Fatalf("after the answers: %d by %d cells, %d writes:\n%s", cols, rows, len(r.raws), r.screen())
	}
	// The cells are 8 by 16 until the terminal says: the picture kept from
	// the half blocks is enough, and nothing is fetched again.
	if got := r.asks(); len(got) != 1 {
		t.Errorf("fetched %q", got)
	}

	// Larger cells: the picture is sent again with more pixels, for which
	// the file is read once more, and the one sent before is forgotten.
	r.send(uv.CellSizeEvent{Width: 16, Height: 32})
	r.settle()
	if len(r.raws) != 3 {
		t.Fatalf("%d writes after the cell size, want the delete and the new picture", len(r.raws))
	}
	first, _ := kittyBytes(t, r.raws[0])
	_, forgot := kittyBytes(t, r.raws[1])
	second, _ := kittyBytes(t, r.raws[2])
	if len(forgot) != 1 || forgot[0] != first[0].id || len(second) != 1 {
		t.Fatalf("forgot %v of %+v, then sent %+v", forgot, first, second)
	}
	// 640 by 360 pixels are 40 by 11.25 cells of 16 by 32: 39 by 11 cells
	// keep the shape inside the box of 44 by 10... by 11 of the picture's own.
	if p := second[0]; p.w != p.cols*16 || p.h != p.rows*32 || p.rows > 10 || p.cols > 44 {
		t.Errorf("for cells of 16 by 32 the terminal was sent %+v", p)
	}
	if cols, rows := r.picture(placeholder); cols != second[0].cols || rows != second[0].rows {
		t.Errorf("the cells are %d by %d, and the picture was sent for %+v", cols, rows, second[0])
	}
	if got := r.asks(); len(got) != 2 {
		t.Errorf("fetched %q, want the file twice", got)
	}
}

// A picture is sent in a way that asks the terminal for no answer. One that
// answers all the same, with an error, is not believed to have no pictures
// at all: once anything was written to it beside the screen, only the
// answers to other questions are taken. Before that, the same words are its
// answer to the question of the start.
func TestAnAnswerToAPictureIsNotAnAnswerToTheStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reply string
		// ids are the pictures the terminal answers about: 31 is the one it
		// was asked about at the start, 32 the one asked about through tmux.
		ids []int
		// writes is how many times the terminal was written to before it
		// answers, and level what it can do after.
		writes int
		level  termcaps.Images
	}{
		{name: "after a picture was sent", reply: chartReply, ids: []int{31, 32}, writes: 1, level: termcaps.ImagesPixels},
		{name: "before any was", reply: "No chart today.", ids: []int{31}, writes: 0, level: termcaps.ImagesBlocks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, id := range tc.ids {
				r := newImageRig(t, termcaps.Caps{}, Options{Env: asked()})
				r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
				r.talk(tc.reply)
				r.send(kittyAnswer(31, "OK"))
				r.send(version("ghostty 1.2.0"))
				r.settle()
				if r.m.caps.Images != termcaps.ImagesPixels || r.m.caps.Settled || len(r.raws) != tc.writes {
					t.Fatalf("before the answer: the level is %v, settled %v, after %d writes", r.m.caps.Images, r.m.caps.Settled, len(r.raws))
				}
				cmd := r.send(kittyAnswer(id, "ENOENT:no such image"))
				r.settle()
				if r.m.caps.Images != tc.level {
					t.Errorf("after the answer about picture %d the level is %v, want %v", id, r.m.caps.Images, tc.level)
				}
				if tc.level != termcaps.ImagesPixels {
					continue
				}
				// Nothing was drawn again for it, and the picture stays.
				if clears(cmd) || len(r.raws) != tc.writes {
					t.Errorf("the answer about picture %d cleared the screen (%v) or wrote to the terminal (%d writes)", id, clears(cmd), len(r.raws))
				}
				if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 {
					t.Errorf("the picture is %d by %d cells:\n%s", cols, rows, r.screen())
				}
				// The other answers of the start are still taken.
				r.send(uv.CellSizeEvent{Width: 16, Height: 32})
				if r.m.caps.CellW != 16 || r.m.caps.CellH != 32 {
					t.Errorf("after the cell size the cells are %d by %d", r.m.caps.CellW, r.m.caps.CellH)
				}
			}
		})
	}
}

// A conversation with pictures in it fits the terminal at every size, at
// both levels: no row is wider than the terminal and there are no more rows
// than it has.
func TestPicturesFitTheTerminal(t *testing.T) {
	t.Parallel()
	for _, caps := range []termcaps.Caps{blockCaps(), pixelCaps()} {
		for _, size := range [][2]int{{100, 30}, {60, 16}, {46, 12}, {40, 12}, {30, 12}, {20, 8}, {10, 5}, {4, 3}, {1, 1}} {
			t.Run(fmt.Sprintf("%v %dx%d", caps.Images, size[0], size[1]), func(t *testing.T) {
				t.Parallel()
				r := newImageRig(t, caps, Options{})
				r.size(size[0], size[1])
				r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
				r.put("icon.png", chartPNG(t, 16, 16), fixedNow().Add(-2*time.Hour))
				r.put("tall.png", chartPNG(t, 90, 900), fixedNow().Add(-2*time.Hour))
				r.talk(chartReply, "![i](icon.png)\n\n![t](tall.png)\n\n![gone](a/very/long/name/of/a/file/that/is/not/there.png)")
				r.settle()
				r.wantFits("pictures", size[0], size[1])
				if got := len(r.rows()); got != size[1] {
					t.Errorf("%d rows on a terminal of %d", got, size[1])
				}
				for _, pg := range []rune{tea.KeyPgUp, tea.KeyPgUp, tea.KeyPgDown} {
					r.key(special(pg))
					r.settle()
					r.wantFits("pictures, scrolled", size[0], size[1])
				}
				if caps.Images == termcaps.ImagesPixels && size[0] >= 20 && size[1] >= 8 && len(r.raws) == 0 {
					t.Errorf("no picture was sent to the terminal:\n%s", r.screen())
				}
			})
		}
	}
}

// Whenever the client has the screen cleared, the pictures are sent to the
// terminal again: it may have dropped them, and the client is never told.
// Nothing is fetched for it. ctrl+l does it from every screen, so a person
// whose pictures have gone has one key that brings them back.
func TestClearingTheScreenSendsThePicturesAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// clear has the screen cleared, and ends in the conversation.
		clear func(r *imageRig) tea.Cmd
	}{
		{name: "ctrl+l", clear: func(r *imageRig) tea.Cmd { return r.key(ctrl('l')) }},
		{name: "/redraw", clear: func(r *imageRig) tea.Cmd { return r.command("/redraw") }},
		{name: "another look", clear: func(r *imageRig) tea.Cmd { return r.command("/theme light") }},
		{name: "ctrl+l in the status line's editor", clear: func(r *imageRig) tea.Cmd {
			r.command("/statusline")
			cmd := r.key(ctrl('l'))
			r.key(esc())
			return cmd
		}},
		{name: "ctrl+l in the rename prompt", clear: func(r *imageRig) tea.Cmd {
			r.command("/rename")
			cmd := r.key(ctrl('l'))
			r.key(esc())
			return cmd
		}},
		{name: "ctrl+l in the list of mods", clear: func(r *imageRig) tea.Cmd {
			r.command("/mods")
			if r.m.screen != screenMods {
				r.t.Fatalf("the screen is %v, want the list of mods", r.m.screen)
			}
			cmd := r.key(ctrl('l'))
			r.key(esc())
			return cmd
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, caps := range []termcaps.Caps{pixelCaps(), blockCaps()} {
				r := newImageRig(t, caps, Options{Mods: newFakeMods()})
				r.put("latency.png", chartPNG(t, 640, 360), fixedNow().Add(-2*time.Hour))
				r.talk(chartReply)
				r.settle()
				was := r.counts()
				if cmd := tc.clear(r); !slices.Contains(inOrder(cmd), tea.ClearScreen()) {
					t.Fatalf("the screen is not cleared")
				}
				r.settle()
				if r.m.screen != screenChat {
					t.Fatalf("the screen is %v", r.m.screen)
				}
				got := r.counts()
				if got.fetched != was.fetched {
					t.Errorf("%v: the file was fetched again: %q", caps.Images, r.asks())
				}
				if caps.Images != termcaps.ImagesPixels {
					if cols, rows := r.picture(halfBlock); cols != 35 || rows != 10 || len(r.raws) != 0 {
						t.Errorf("half blocks: the picture is %d by %d cells after %d writes:\n%s", cols, rows, len(r.raws), r.screen())
					}
					continue
				}
				// The old picture is forgotten, then the new one sent.
				if len(r.raws) != 3 {
					t.Fatalf("%d writes to the terminal, want the picture, the delete and the picture again", len(r.raws))
				}
				first, _ := kittyBytes(t, r.raws[0])
				none, forgot := kittyBytes(t, r.raws[1])
				second, kept := kittyBytes(t, r.raws[2])
				if len(none) != 0 || len(forgot) != 1 || forgot[0] != first[0].id || len(second) != 1 || len(kept) != 0 {
					t.Errorf("after %+v the terminal was told to forget %v and sent %+v", first, forgot, second)
				}
				if cols, rows := r.picture(placeholder); cols != 35 || rows != 10 {
					t.Errorf("the picture is %d by %d cells:\n%s", cols, rows, r.screen())
				}
				if !strings.Contains(r.m.View().Content, fmt.Sprintf("\x1b[38;5;%dm", second[0].id)) {
					t.Errorf("the cells do not name picture %d", second[0].id)
				}
			}
		})
	}
}

// pictures is a fake server whose contact ada has these files, each the same
// small chart.
func pictures(t *testing.T, names ...string) *fakeBackend {
	t.Helper()
	b := &fakeBackend{
		contacts: []client.Contact{{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}},
		files:    map[string]client.File{},
	}
	for _, name := range names {
		b.files[name] = client.File{Data: chartPNG(t, 640, 360), ETag: `"` + name + `"`, Modified: fixedNow().Add(-2 * time.Hour)}
	}
	return b
}

// chartPage is a page of the log long enough to scroll: forty messages, a
// reply that names these pictures, and a last word.
func chartPage(t *testing.T, names ...string) client.Update {
	t.Helper()
	at := fixedNow().Add(-time.Hour)
	var reply strings.Builder
	reply.WriteString("Here they are.")
	for _, name := range names {
		reply.WriteString("\n\n![a chart](" + name + ")")
	}
	page := messages(t, 1, 40, at)
	page.Events = append(page.Events,
		logged(t, 41, at, core.KindAgentMsg, core.MsgBody{Text: reply.String()}),
		logged(t, 42, at, core.KindUserMsg, core.MsgBody{Text: "thanks"}),
	)
	return page
}

// Under a real program the picture is written to the terminal once, before
// the cells that show it, and not again when the conversation is scrolled or
// the terminal resized: the cells are text, and the terminal keeps the
// picture. The files are fetched one at a time.
func TestTheProgramSendsAPictureOnce(t *testing.T) {
	t.Parallel()
	env, over := terminal(t, pixelCaps())
	r := startProgram(t, Options{Env: env, Overrides: over, Backend: pictures(t, "latency.png", "errors.png")})
	untilShown(t, r.out, 0, "▸ ada")
	r.write("\r")
	untilShown(t, r.out, 0, "Message ada")
	const transmit, forget = "\x1b_Ga=T,U=1,q=2,f=100,t=d,i=", "\x1b_Ga=d"
	sentTwice := func() bool { return strings.Count(string(r.out.Since(0)), transmit) == 2 }

	// up says both pictures were sent and the cells of each were drawn.
	drawn := regexp.MustCompile(`\x1b\[38;5;\d+m` + placeholder)
	up := func() bool {
		return sentTwice() && len(slices.Compact(slices.Sorted(slices.Values(drawn.FindAllString(string(r.out.Since(0)), -1))))) == 2
	}
	r.p.Send(updatesMsg{sess: r.b.sessions[0], batch: []client.Update{live(), chartPage(t, "latency.png", "errors.png")}})
	for range 50 {
		r.out.Quiet(t)
		if up() {
			break
		}
	}
	wrote := string(r.out.Since(0))
	if !up() {
		t.Fatalf("the two pictures were sent %d times, and the cells of %d drawn:\n%q", strings.Count(wrote, transmit), len(drawn.FindAllString(wrote, -1)), wrote[:min(len(wrote), 4000)])
	}
	// Each picture is in the terminal before the first cell that shows it.
	for id := range strings.SplitSeq(wrote, transmit) {
		n, _, _ := strings.Cut(id, ",")
		if _, err := strconv.Atoi(n); err != nil {
			continue // what was written before the first picture
		}
		sent := strings.Index(wrote, transmit+n+",")
		cells := strings.Index(wrote, "\x1b[38;5;"+n+"m"+placeholder)
		if cells < 0 || cells < sent {
			t.Errorf("picture %s was sent at byte %d and its cells drawn at %d", n, sent, cells)
		}
	}
	// A cell is the placeholder and its two marks, as the client wrote it.
	if !regexp.MustCompile(placeholder + `\p{Mn}\p{Mn}` + placeholder + `\p{Mn}\p{Mn}`).MatchString(wrote) {
		t.Errorf("the cells lost their marks on the way to the terminal:\n%q", wrote[strings.Index(wrote, placeholder):][:200])
	}

	// Scrolled out of view and back, then a larger terminal with the same
	// box: the cells are drawn again, the pictures are not sent again. The
	// program writes only the cells that change, so of a page that moved
	// there may be no more to find than a digit: the mark that the view left
	// the newest line is what is waited for.
	mark := r.out.Mark()
	r.write("\x1b[5~")
	untilShown(t, r.out, mark, "↓ newer")
	r.write("\x1b[5~")
	r.out.Quiet(t)
	mark = r.out.Mark()
	r.write("\x1b[6~\x1b[6~")
	until(t, r.out, mark, placeholder)
	mark = r.out.Mark()
	r.p.Send(tea.WindowSizeMsg{Width: 120, Height: 32})
	until(t, r.out, mark, placeholder)
	r.out.Quiet(t)
	r.quit()

	wrote = string(r.out.Since(0))
	if got := strings.Count(wrote, transmit); got != 2 {
		t.Errorf("after scrolling and a resize the pictures were sent %d times, want 2", got)
	}
	if strings.Contains(wrote, forget) {
		t.Errorf("a picture was forgotten while its conversation was open")
	}
	if r.b.most != 1 || len(r.b.asked) != 2 {
		t.Errorf("%d files were fetched, at most %d at once", len(r.b.asked), r.b.most)
	}
	if st := r.m.chat.imgs.store.Stats(); st.Jobs != 2 || st.Encodes != 2 {
		t.Errorf("the store did %+v, want two jobs and two pictures encoded", st)
	}
}

// When the interface ends, the terminal still holds the pictures it was
// sent: Run tells it to forget them, on the output it was given, after the
// program has gone. A conversation that was left before has had its pictures
// forgotten by then, through the program.
func TestRunForgetsThePicturesWhenItEnds(t *testing.T) {
	t.Parallel()
	const transmit = "\x1b_Ga=T,U=1,q=2,f=100,t=d,i="
	for _, tc := range []struct {
		name string
		// keys are pressed once the picture is up, and end the run.
		keys string
		// after says the delete is written after the program, not by it.
		after bool
	}{
		{name: "left from the conversation", keys: "\x03", after: true},
		{name: "left from the list", keys: "\x10q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := pictures(t, "latency.png")
			ada := b.contacts[0]
			in, keys := io.Pipe()
			t.Cleanup(func() { _ = keys.Close() }) // closing a pipe's writer never fails
			out, late := &tuitest.Recorder{}, &tuitest.Recorder{}
			env, over := terminal(t, pixelCaps())
			done := make(chan error, 1)
			go func() {
				done <- Run(t.Context(), Options{
					Backend: b, Open: &ada, Now: fixedNow, Location: time.UTC, Env: env, Overrides: over, Output: late,
					Program: []tea.ProgramOption{
						tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(100, 30),
						tea.WithEnvironment([]string{"TERM=xterm-256color"}),
					},
				})
			}()
			untilShown(t, out, 0, "Message ada")
			sess := b.sessions[0]
			sess.updates <- live()
			sess.updates <- chartPage(t, "latency.png")
			until(t, out, 0, placeholder)
			if len(late.Since(0)) != 0 {
				t.Fatalf("while the program runs %q was written past it", late.Since(0))
			}
			wrote := string(out.Since(0))
			id, _, _ := strings.Cut(wrote[strings.Index(wrote, transmit)+len(transmit):], ",")
			forget := "\x1b_Ga=d,d=I,i=" + id + ",q=2\x1b\\"

			if _, err := keys.Write([]byte(tc.keys[:1])); err != nil {
				t.Fatal(err)
			}
			if !tc.after {
				// The list is up and the terminal was told, by the program.
				until(t, out, 0, forget)
				if _, err := keys.Write([]byte(tc.keys[1:])); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				t.Fatalf("Run returned %v", err)
			}
			through, past := string(out.Since(0)), string(late.Since(0))
			if tc.after {
				if past != forget || strings.Contains(through, "\x1b_Ga=d") {
					t.Errorf("after the program %q was written, and through it a delete: %v; want only %q after it", past, strings.Contains(through, "\x1b_Ga=d"), forget)
				}
				return
			}
			if past != "" || strings.Count(through, forget) != 1 {
				t.Errorf("after the program %q was written, and the delete %d times through it", past, strings.Count(through, forget))
			}
		})
	}
}
