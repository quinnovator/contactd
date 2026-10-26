package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/commands"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/scrollback"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/termimg"
)

// The pictures of a conversation. A reply names a file of its contact's the
// way Markdown names an image. The conversation's renderer asks for it here
// while it draws, and is answered at once: with a line of text until the file
// has been read, and then with the rows that show it, as the terminal can.
// Reading and decoding a file is slow and is never done on the update loop:
// the store hands it out as a job, which runs in a command, one at a time.
//
// At the best level the picture itself is sent to the terminal, outside the
// screen, and the screen holds only the cells that show it. Those bytes, and
// the ones that make the terminal forget a picture again, are the only ones
// this client writes beside the screen once it is up.

const (
	// imageCache is how many bytes of scaled-down pictures one conversation
	// keeps.
	imageCache = 16 << 20
	// imageIDs is how many picture ids a terminal has for this client.
	imageIDs = 255
	// saveName is the most characters of a file's own name a saved copy
	// keeps, which is as many as the conversation shows of it.
	saveName = 80
	// slash ends a path that names a directory.
	slash = string(filepath.Separator)
)

// images has the pictures of one conversation's replies. It is the
// draw.ImageSource of the conversation's renderer.
type images struct {
	store *termimg.Store
	// fetch reads one file of the contact's. ctx is done when the
	// conversation is left, which stops a job that is still running.
	fetch  termimg.Fetch
	ctx    context.Context //nolint:containedctx // a job runs in a command, which is handed no context; this one is the conversation's
	cancel context.CancelFunc
	// held are the replies whose picture waits to be written to the
	// terminal, and going those whose picture is being written. Each is
	// drawn again once the terminal has its picture: the cells that show a
	// picture are no use before it.
	held, going []uint64
	// up says which pictures were last drawn as a picture, and not as a line.
	up map[imageKey]bool
}

// imageKey names one picture of a conversation: its reply, and its number
// in that reply.
type imageKey struct {
	seq uint64
	n   int
}

// Image implements draw.ImageSource. A reply with no sequence number has no
// name the store could keep its picture under, and stays a line of text.
//
// The store hands out the cells of a picture from the moment its job is
// done. The terminal may not have the picture yet: it is written after
// whatever is being written. Should the reply be drawn in between, for
// another picture of its own or a key that draws everything again, the
// picture that is still on its way stays the line it was while it loaded.
func (im *images) Image(ref draw.ImageRef, maxCols, maxRows int) draw.Shown {
	if ref.Seq == 0 {
		return draw.Shown{}
	}
	shown := draw.Shown(im.store.Show(termimg.Ref(ref), maxCols, maxRows))
	key := imageKey{seq: ref.Seq, n: ref.N}
	if len(shown.Rows) > 0 && !im.up[key] && (slices.Contains(im.held, ref.Seq) || slices.Contains(im.going, ref.Seq)) {
		return draw.Shown{}
	}
	im.up[key] = len(shown.Rows) > 0
	return shown
}

// newImages makes the pictures of a conversation with one contact. The first
// picture id is taken from the clock: two clients in one terminal share its
// ids, and should not start at the same one.
func (m *model) newImages(id core.ContactID) *images {
	ctx, cancel := context.WithCancel(m.ctx)
	im := &images{
		store: termimg.NewStore(termimg.StoreOptions{
			Limits: termimg.DefaultLimits(), CacheBytes: imageCache,
			IDBase:  int(m.opt.Now().UnixMilli() % imageIDs),
			MaxCols: 120, MaxRows: 40,
		}),
		fetch: fetcher(m.opt.Backend, id), ctx: ctx, cancel: cancel,
		up: map[imageKey]bool{},
	}
	im.store.SetCaps(m.caps)
	return im
}

// fetcher reads a contact's files from the server for the store. The store
// tells a file that is not there and one that is too large from one that
// could not be read this time, so those two are named the way it knows them.
func fetcher(b Backend, id core.ContactID) termimg.Fetch {
	return func(ctx context.Context, file, etag string) (termimg.FileData, error) {
		f, err := b.File(ctx, id, file, etag)
		switch {
		case errors.Is(err, client.ErrFileNotFound):
			return termimg.FileData{}, fmt.Errorf("%w: %w", termimg.ErrNotFound, err)
		case errors.Is(err, client.ErrFileTooLarge):
			return termimg.FileData{}, fmt.Errorf("%w: %w", termimg.ErrTooLarge, err)
		case err != nil:
			return termimg.FileData{}, fmt.Errorf("fetch %s: %w", file, err)
		}
		return termimg.FileData{Data: f.Data, ETag: f.ETag, Modified: f.Modified, NotModified: f.NotModified}, nil
	}
}

// leave ends the pictures of a conversation that is being left: a job that is
// still running is stopped, and the bytes returned make the terminal forget
// every picture it was sent.
func (im *images) leave() string {
	im.cancel()
	im.held, im.going = nil, nil
	return im.store.DeleteAll()
}

// redraw has replies drawn again whose pictures have changed: one was read,
// one failed, or the terminal has one now.
func (c *chat) redraw(seqs []uint64) {
	for _, seq := range seqs {
		c.list.rd.Forget(seq)
		c.win.Invalidate(scrollback.Key{Kind: blockItem, ID: seq})
		c.dirty |= dirtyBody
	}
	// A picture on the stage is one of them, or none is.
	if len(seqs) > 0 && c.stage != nil {
		c.stage.Refresh()
	}
}

// setCaps tells the conversation's pictures what the terminal can do now.
// When that changes how a picture is drawn, the replies that hold one are
// drawn again, and the bytes returned make the terminal forget the pictures
// it was sent the old way.
func (c *chat) setCaps(caps termcaps.Caps) string {
	changed, raw := c.imgs.store.SetCaps(caps)
	c.redraw(changed)
	return raw
}

// imageDoneMsg is what a job made of one picture. sess is the conversation
// whose picture it is.
type imageDoneMsg struct {
	sess Session
	done termimg.Done
}

// imageJob starts the next job of the open conversation's pictures, if there
// is one and none is running. A picture is wanted from the moment its reply
// is drawn, so the conversation is drawn here when it is stale: what it wants
// is then known now and not after the next message. The view the program
// asks for next is the one drawn here.
func (m *model) imageJob() tea.Cmd {
	c := m.chat
	if c == nil {
		return nil
	}
	if m.screen == screenChat && c.dirty != 0 {
		c.draw(m.opt.Now())
	}
	job, ok := c.imgs.store.Next()
	if !ok {
		return nil
	}
	ctx, fetch, sess := c.imgs.ctx, c.imgs.fetch, c.sess
	return func() tea.Msg { return imageDoneMsg{sess: sess, done: job(ctx, fetch)} }
}

// onImageDone takes what a job made. The reply is drawn again: with the
// picture, or with the line that says why there is none. A picture that is
// sent to the terminal is written first, and its reply is drawn again when
// that is done. What a job made for a conversation that has been left went
// with it.
func (m *model) onImageDone(msg imageDoneMsg) {
	c := m.chat
	if c == nil || c.sess != msg.sess {
		return
	}
	seq, raw := c.imgs.store.Finish(msg.done)
	// A picture that took its id from another leaves that one's reply to be
	// drawn again too.
	seqs := c.imgs.store.Stale()
	if seq != 0 {
		seqs = append(seqs, seq)
	}
	if raw == "" {
		c.redraw(seqs)
		return
	}
	m.write(raw)
	c.imgs.held = append(c.imgs.held, seqs...)
}

// rawOut is what the client writes to the terminal beside the screen: the
// pictures, and the word to forget them. It goes out one write at a time and
// in the order it was queued, so that a picture is never forgotten before it
// was sent.
type rawOut struct {
	queue []string
	// busy means a write is on its way to the terminal.
	busy bool
	// used means something was queued for the terminal at least once. What
	// the terminal says about pictures from then on may be about one of
	// these writes, and is no longer its answer to the question of the start.
	used bool
}

// rawWrittenMsg says the bytes of the last write have been handed to the
// terminal.
type rawWrittenMsg struct{}

// write queues bytes for the terminal. flush sends them.
func (m *model) write(raw string) {
	if raw != "" {
		m.raw.queue, m.raw.used = append(m.raw.queue, raw), true
	}
}

// flush sends what is queued for the terminal, unless a write is still on
// its way. The program writes the bytes before it draws the next screen, and
// only then is the next message handled.
func (m *model) flush() tea.Cmd {
	if m.raw.busy || len(m.raw.queue) == 0 {
		return nil
	}
	raw := strings.Join(m.raw.queue, "")
	m.raw.queue, m.raw.busy = nil, true
	if c := m.chat; c != nil {
		c.imgs.going, c.imgs.held = c.imgs.held, nil
	}
	return tea.Sequence(tea.Raw(raw), func() tea.Msg { return rawWrittenMsg{} })
}

// onRawWritten is called when the terminal has what was written. The replies
// whose pictures were in it are drawn again, now with the cells that show
// them.
func (m *model) onRawWritten() {
	m.raw.busy = false
	if c := m.chat; c != nil {
		c.redraw(c.imgs.going)
		c.imgs.going = nil
	}
}

// clear has the whole screen cleared and drawn again, which is what ctrl+l
// asks for. The pictures are part of it: they are sent again.
func (m *model) clear() tea.Cmd {
	m.resend()
	return tea.ClearScreen
}

// resend has the pictures of the open conversation sent to the terminal
// again, each as it next comes into view. It goes with every clearing of the
// screen the client asks for, so that ctrl+l brings back pictures the
// terminal no longer holds. The client is never told that one is gone: a
// terminal drops the oldest when it holds too many, and inside tmux it need
// not be the terminal that was sent them. Nothing is fetched for it.
//
// Erasing the screen is not what loses a picture: kitty and Ghostty keep the
// ones sent this way, so a resize, for which the program erases the screen
// too, sends nothing. A picture sent again here can reach the terminal
// before the erase that the same key asked for.
func (m *model) resend() {
	c := m.chat
	if c == nil {
		return
	}
	m.write(c.imgs.store.DeleteAll())
	c.win.InvalidateAll()
	c.restaged()
	c.dirty |= dirtyTail | dirtyBody
}

// cmdImage is /image: how pictures are shown in this session, or a copy of
// one picture's file.
func cmdImage(m *model, l commands.Line) tea.Cmd {
	if len(l.Args) > 0 && strings.EqualFold(l.Args[0], "save") {
		return cmdImageSave(m, l)
	}
	return cmdSetting(m, l)
}

// imageSavedMsg says what became of /image save. sess is the conversation
// that asked and draft the line that did, which goes back into the box when
// nothing was saved.
type imageSavedMsg struct {
	sess  Session
	draft string
	note  string
	saved bool
}

// cmdImageSave is "/image save <n> [path]": the file of a picture that was
// shown is fetched again and written beside the person, under its own name
// in the directory the client was started in, or where the path says. What
// is written is the file's own bytes, not the picture the client drew. A file
// that is already there is never written over. The person asked for the
// file, so it is fetched and written also when the conversation is left
// meanwhile.
func cmdImageSave(m *model, l commands.Line) tea.Cmd {
	c := m.chat
	if len(l.Args) < 2 || len(l.Args) > 3 {
		return m.wrong(l)
	}
	seq, n, ok := imageNumber(l.Args[1])
	if !ok {
		return m.wrong(l)
	}
	ref, ok := c.imgs.store.Lookup(seq, n)
	if !ok {
		return m.refuse("no image " + safe.Line(l.Args[1], nameInNote, false) + " has been shown in this conversation")
	}
	to := ""
	if len(l.Args) == 3 && l.Args[2] != "" {
		to = m.typedPath(l.Args[2])
		// A path that ends in a slash names a directory, there or not.
		if last := l.Args[2][len(l.Args[2])-1]; (last == '/' || last == filepath.Separator) && !strings.HasSuffix(to, slash) {
			to += slash
		}
	}
	b, ctx, id, sess := m.opt.Backend, m.ctx, c.contact.ID, c.sess
	home, workDir, draft := m.opt.Home, m.opt.WorkDir, "/"+l.Name+" "+l.Rest
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		note, err := saveImage(ctx, b, id, ref.Path, to, workDir, home)
		if err != nil {
			return imageSavedMsg{sess: sess, draft: draft, note: "not saved: " + err.Error()}
		}
		return imageSavedMsg{sess: sess, note: note, saved: true}
	}
}

// onImageSaved says where a picture's file went, or why it went nowhere.
// The person is told wherever they are by now: the file is on their disk.
func (m *model) onImageSaved(msg imageSavedMsg) {
	if msg.saved {
		m.say(msg.note, toneDone)
		return
	}
	if c := m.chat; c != nil && c.sess == msg.sess {
		c.restore(msg.draft)
	}
	m.say(msg.note, "")
}

// imageNumber reads what /image calls a picture: the sequence number of its
// reply, and after a dot the picture's number in that reply when it is not
// the first. "31" and "31.2".
func imageNumber(s string) (seq uint64, n int, ok bool) {
	head, tail, more := strings.Cut(s, ".")
	seq, err := strconv.ParseUint(head, 10, 64)
	if err != nil || seq == 0 {
		return 0, 0, false
	}
	if !more {
		return seq, 1, true
	}
	n, err = strconv.Atoi(tail)
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return seq, n, true
}

// saveImage fetches a contact's file and writes it to the person's disk. to
// is where, as the person named it, or "" for the file's own name in workDir.
// A directory takes the file under its own name. It returns the line that
// says where the file went; an error says, in words for the person, why it
// went nowhere.
//
// The name is a reply's, so a model chose it. It is used as the conversation
// shows it, with nothing that is not drawn, and a name that would make a
// hidden file is used only where the person typed the name themselves.
func saveImage(ctx context.Context, b Backend, id core.ContactID, file, to, workDir, home string) (string, error) {
	name := safe.Line(path.Base(file), saveName, false)
	if name == "" {
		name = "file"
	}
	own := func(dir string) (string, error) {
		if strings.HasPrefix(name, ".") {
			return "", fmt.Errorf("%s would be a hidden file; name the file to save it as", name)
		}
		return filepath.Join(dir, name), nil
	}
	var err error
	if to == "" {
		to, err = own(workDir)
	} else if info, statErr := os.Stat(to); strings.HasSuffix(to, slash) || statErr == nil && info.IsDir() {
		to, err = own(to)
	}
	if err != nil {
		return "", err
	}
	shown := conf.Short(to, home)
	if rel, relErr := filepath.Rel(workDir, to); workDir != "" && relErr == nil && filepath.IsLocal(rel) {
		shown = "./" + filepath.ToSlash(rel)
	}

	f, err := b.File(ctx, id, file, "")
	switch {
	case errors.Is(err, client.ErrFileNotFound):
		return "", fmt.Errorf("the contact has no file %s any more", name)
	case errors.Is(err, client.ErrFileTooLarge):
		return "", fmt.Errorf("%s is larger than the server sends", name)
	case err != nil:
		return "", errors.New(explain(err))
	}
	if err := writeNew(to, f.Data); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%s is already there, and is not written over", shown)
		}
		// The error names the whole path; the person is told the short one.
		if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
			return "", fmt.Errorf("%s: %w", shown, pe.Err)
		}
		return "", err
	}
	return fmt.Sprintf("saved %s to %s, %d bytes", name, shown, len(f.Data)), nil
}

// writeNew writes a file that must not exist yet, for its owner alone. A
// file that could not be written whole is taken away again.
func writeNew(name string, data []byte) error {
	//nolint:gosec // the person named the path, or it is the file's shown name in the directory the client was started in
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("save: %w", err)
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name) // half a file is worse than none; the write's error is the one to report
		return fmt.Errorf("save: %w", err)
	}
	return nil
}
