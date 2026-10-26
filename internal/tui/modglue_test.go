package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/statusline"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// modCall is one request the fake host was sent.
type modCall struct {
	mod, op, name string
	in            mods.Input
}

// fakeMods stands where the mod host would: it runs no module, remembers
// what it was asked, and answers each call with what the test gave it. It
// also writes down every call made to it from the update loop, where the
// interface must make none.
type fakeMods struct {
	mu     sync.Mutex
	infos  []mods.Info
	calls  []modCall
	answer map[string]func(mods.Input) (wire.Reply, error)
	// did is what was done to the mods, such as "on git", and fail what
	// doing it comes to when that is an error.
	did  []string
	fail map[string]error
	// dist is the mods there are to install, by directory.
	dist map[string]mods.Manifest
	// changed is the version Update finds, by mod, where it finds a newer
	// one. broken is why a mod cannot be loaded, by mod.
	changed map[string]string
	broken  map[string]string
	loads   int
	closed  int
	// loop says Update is running, and onLoop what was called meanwhile.
	loop   bool
	onLoop []string
}

// newFakeMods is a host with these mods installed, each switched on and not
// loaded yet.
func newFakeMods(manifests ...mods.Manifest) *fakeMods {
	f := &fakeMods{
		answer: map[string]func(mods.Input) (wire.Reply, error){},
		fail:   map[string]error{}, dist: map[string]mods.Manifest{},
		changed: map[string]string{}, broken: map[string]string{},
	}
	for _, man := range manifests {
		f.infos = append(f.infos, mods.Info{Manifest: man, Status: mods.StatusLoading})
	}
	f.sort()
	return f
}

func (f *fakeMods) sort() {
	slices.SortFunc(f.infos, func(a, b mods.Info) int { return strings.Compare(a.Manifest.Name, b.Manifest.Name) })
}

// called writes a call down when it comes from the update loop. f.mu is
// held.
func (f *fakeMods) called(what string) {
	if f.loop {
		f.onLoop = append(f.onLoop, what)
	}
}

// find is the mod of a name. f.mu is held.
func (f *fakeMods) find(name string) *mods.Info {
	for i := range f.infos {
		if f.infos[i].Manifest.Name == name {
			return &f.infos[i]
		}
	}
	return nil
}

// set gives a mod a status and a reason.
func (f *fakeMods) set(name string, status mods.Status, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info := f.find(name); info != nil {
		info.Status, info.Reason = status, reason
	}
}

// answers sets what a mod answers one kind of request with from now on.
func (f *fakeMods) answers(mod, op, name string, fn func(mods.Input) (wire.Reply, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer[mod+" "+op+" "+name] = fn
}

// says is answers for a reply that is always the same.
func (f *fakeMods) says(mod, op, name string, reply wire.Reply) {
	f.answers(mod, op, name, func(mods.Input) (wire.Reply, error) { return reply, nil })
}

// sent is every request of one kind sent so far, in order.
func (f *fakeMods) sent(mod, op, name string) []modCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []modCall
	for _, c := range f.calls {
		if c.mod == mod && c.op == op && c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// count is how many requests were sent so far, of every kind.
func (f *fakeMods) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// done is what was done to the mods so far.
func (f *fakeMods) done() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.did)
}

func (f *fakeMods) Load(context.Context) []mods.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Load")
	f.loads++
	for i := range f.infos {
		if f.infos[i].Status == mods.StatusLoading {
			f.infos[i].Status = mods.StatusOn
		}
	}
	return slices.Clone(f.infos)
}

func (f *fakeMods) Mods() []mods.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Mods")
	return slices.Clone(f.infos)
}

func (f *fakeMods) Needs() []mods.Grant {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Needs")
	var out []mods.Grant
	for _, grant := range mods.Grants() {
		if slices.ContainsFunc(f.infos, func(info mods.Info) bool {
			return info.Status == mods.StatusOn && info.Manifest.Has(grant)
		}) {
			out = append(out, grant)
		}
	}
	return out
}

func (f *fakeMods) Call(_ context.Context, mod, op, name string, in mods.Input) (wire.Reply, error) {
	f.mu.Lock()
	f.called("Call")
	info := f.find(mod)
	if info == nil || info.Status != mods.StatusOn {
		f.mu.Unlock()
		return wire.Reply{}, fmt.Errorf("mod %s is not on", mod)
	}
	f.calls = append(f.calls, modCall{mod: mod, op: op, name: name, in: in})
	fn := f.answer[mod+" "+op+" "+name]
	f.mu.Unlock()
	if fn == nil {
		return wire.Reply{ABI: wire.ABI}, nil
	}
	return fn(in)
}

func (f *fakeMods) SetEnabled(_ context.Context, name string, on bool) (mods.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("SetEnabled")
	word := "off " + name
	if on {
		word = "on " + name
	}
	f.did = append(f.did, word)
	info := f.find(name)
	if info == nil {
		return mods.Info{}, fmt.Errorf("no mod named %q", name)
	}
	if err := f.fail[word]; err != nil {
		return *info, err
	}
	info.Status, info.Reason = mods.StatusOff, ""
	switch why := f.broken[name]; {
	case on && why != "":
		// A mod that cannot be loaded is no error: its status says so.
		info.Status, info.Reason = mods.StatusFailed, why
	case on:
		info.Status = mods.StatusOn
	}
	return *info, nil
}

func (f *fakeMods) Install(_ context.Context, dir string) (mods.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Install")
	f.did = append(f.did, "install "+dir)
	man, ok := f.dist[dir]
	if !ok {
		return mods.Info{}, fmt.Errorf("install: open %s: no such file or directory", filepath.Join(dir, mods.ManifestFile))
	}
	if info := f.find(man.Name); info != nil {
		*info = mods.Info{Manifest: man, Status: mods.StatusOff}
		return *info, nil
	}
	f.infos = append(f.infos, mods.Info{Manifest: man, Status: mods.StatusOff})
	f.sort()
	return *f.find(man.Name), nil
}

func (f *fakeMods) Update(_ context.Context, name string) (mods.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Update")
	f.did = append(f.did, "update "+name)
	info := f.find(name)
	if info == nil {
		return mods.Info{}, fmt.Errorf("no mod named %q", name)
	}
	if err := f.fail["update "+name]; err != nil {
		return *info, err
	}
	if version := f.changed[name]; version != "" {
		info.Manifest.Version = version
		info.Status, info.Reason = mods.StatusOff, "updated: switch it on to use the new version"
	}
	return *info, nil
}

func (f *fakeMods) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called("Close")
	f.closed++
	return nil
}

// The mods of the tests. cacheMod, gitMod and standupMod are the shipped
// examples as their manifests declare them.
func cacheMod() mods.Manifest {
	return mods.Manifest{
		ABI: wire.ABI, Name: "cache", Version: "0.3.1", Module: "cache.wasm",
		Description: "How much of each prompt the provider served from its cache.",
		Read:        []mods.Grant{mods.GrantModelCalls},
		Segments:    []mods.SegmentDecl{{Name: "cache", Refresh: mods.RefreshModelCall}},
		Commands:    []mods.CommandDecl{{Name: "stats", Usage: "[days]", Summary: "token use and cache share by day"}},
	}
}

func gitMod() mods.Manifest {
	return mods.Manifest{
		ABI: wire.ABI, Name: "git", Version: "0.1.0", Module: "git.wasm",
		Description: "The git branch checked out where the client started.",
		Read:        []mods.Grant{mods.GrantWorkspace},
		Segments:    []mods.SegmentDecl{{Name: "branch", Refresh: mods.RefreshTurn}},
	}
}

func standupMod() mods.Manifest {
	return mods.Manifest{
		ABI: wire.ABI, Name: "standup", Version: "0.2.0", Module: "standup.wasm",
		Description: "Drafts a message that asks for yesterday's work, ready to edit and send.",
		Read:        []mods.Grant{mods.GrantClock},
		Commands:    []mods.CommandDecl{{Name: "standup", Summary: "summarize yesterday for the team"}},
	}
}

// branchAnswer is the git mod's segment: the branch it is told.
func branchAnswer(in mods.Input) (wire.Reply, error) {
	ws := in.Data.Workspace
	if ws == nil || ws.Branch == "" {
		return wire.Reply{ABI: wire.ABI}, nil
	}
	if in.Term.Glyphs == "ascii" {
		return wire.Reply{ABI: wire.ABI, Text: ws.Branch, Short: ws.Branch}, nil
	}
	return wire.Reply{ABI: wire.ABI, Text: "⎇ " + ws.Branch, Short: ws.Branch}, nil
}

// cacheAnswer is a segment that counts the model calls it is told of.
func cacheAnswer(in mods.Input) (wire.Reply, error) {
	n := strconv.Itoa(len(in.Data.ModelCalls))
	return wire.Reply{ABI: wire.ABI, Text: "calls " + n, Short: n}, nil
}

// repo makes a directory that is a git repository with a branch checked out,
// as far as reading its HEAD goes.
func repo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	checkout(t, dir, branch)
	return dir
}

// checkout puts another branch in a repository's HEAD.
func checkout(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// modRig is a harness over a fake mod host. Every command Update answers
// with is kept and run by the test, so that nothing a mod was asked is lost;
// timers are written down and never go off by themselves.
type modRig struct {
	*harness

	host   *fakeMods
	timers []timer
}

// newModRig makes the interface over a mod host, 100 columns by 30 rows,
// for a terminal that can do caps, with a config directory under the home
// directory. A nil host is a client started without mods.
func newModRig(t *testing.T, caps termcaps.Caps, host *fakeMods, opt Options) *modRig {
	t.Helper()
	r := &modRig{host: host}
	// A test that gives an environment of its own gets that terminal.
	if opt.Env == (termcaps.Env{}) {
		opt.Env, opt.Overrides = terminal(t, caps)
	}
	if opt.Dirs.Config == "" {
		home := t.TempDir()
		opt.Dirs, opt.Home = conf.Dirs{Config: filepath.Join(home, "contactd")}, home
	}
	if host != nil {
		opt.Mods = host
	}
	r.harness = newHarnessWith(t, opt, client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	r.keep = true
	r.m.after = func(wait time.Duration, msg tea.Msg) tea.Cmd {
		r.timers = append(r.timers, timer{wait: wait, msg: msg})
		return nil
	}
	if host != nil {
		// Nothing of the host is ever called from Update.
		r.around = func(in bool) {
			host.mu.Lock()
			host.loop = in
			host.mu.Unlock()
		}
		t.Cleanup(func() {
			host.mu.Lock()
			defer host.mu.Unlock()
			if len(host.onLoop) > 0 {
				t.Errorf("the host was called from the update loop: %q", host.onLoop)
			}
		})
	}
	r.size(100, 30)
	return r
}

// started runs the commands kept so far, and returns what they gave. That is
// the calls to the mods being made; their answers are not delivered yet.
func (r *modRig) started() []tea.Msg {
	r.t.Helper()
	cmds := r.kept
	r.kept = nil
	var out []tea.Msg
	for _, cmd := range cmds {
		for _, msg := range inOrder(cmd) {
			if msg != nil {
				out = append(out, msg)
			}
		}
	}
	return out
}

// settle runs the commands kept so far and delivers what they give, and so
// on until nothing is on its way.
func (r *modRig) settle() {
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

// load has the host load its mods, as the program does at its start.
func (r *modRig) load() {
	r.t.Helper()
	r.kept = append(r.kept, r.m.loadMods())
	r.settle()
}

// talk opens a conversation with ada, connected and with a little history.
// The session has nothing more to say by itself: the test says it.
func (r *modRig) talk() *fakeSession {
	r.t.Helper()
	r.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	sess := r.b.sessions[len(r.b.sessions)-1]
	close(sess.updates)
	r.from(sess, live(), history(r.t))
	r.settle()
	return sess
}

// attempt is a page of the log with one more model call.
func attempt(t *testing.T, seq uint64) client.Update {
	t.Helper()
	return client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, seq, fixedNow(), core.KindAttempt, core.AttemptBody{Route: "fake/fake-1", Input: 1000, Cached: 500, Output: 10, MS: 900}),
	}}
}

// typing is the stream saying a reply is being written, or that the turn is
// over.
func typing(on bool) client.Update {
	return delta(core.Delta{Type: core.DeltaTyping, Typing: on})
}

// A client that was handed mods loads them in a command, away from the
// update loop, and until they are loaded nothing of them is in the
// interface. Once they are, the segment of one is in the bar, before the
// clock, with what the mod answered.
func TestAModSegmentIsDrawnInTheBar(t *testing.T) {
	t.Parallel()
	host := newFakeMods(gitMod())
	host.answers("git", mods.OpSegment, "branch", branchAnswer)
	r := newModRig(t, trueDark(), host, Options{WorkDir: repo(t, "main")})
	if host.loads != 0 || host.count() != 0 {
		t.Fatalf("before the program runs the host was asked to load %d times and called %d times", host.loads, host.count())
	}
	// The command is there to run; making it loaded nothing.
	load := r.m.loadMods()
	if load == nil || host.loads != 0 {
		t.Fatalf("the load is %v, and the host has loaded %d times", load, host.loads)
	}
	// A conversation opened before the mods are loaded has the bar it would
	// have without them.
	sess := r.talk()
	r.wantBar("before the mods are loaded", defaultLeft, defaultRight)
	if host.count() != 0 {
		t.Errorf("a mod that is not loaded was called %d times", host.count())
	}

	r.kept = append(r.kept, load)
	msgs := r.started()
	if len(msgs) != 1 || host.loads != 1 {
		t.Fatalf("the load gave %v after %d loads", msgs, host.loads)
	}
	// The mods are loaded, and the bar has not heard yet.
	r.wantBar("while the answer is on its way", defaultLeft, defaultRight)
	r.send(msgs[0])
	// The segment is asked for in a command, and shows nothing until then.
	r.wantBar("asked, not answered", defaultLeft, defaultRight)
	calls := r.started()
	if len(calls) != 1 || len(host.sent("git", mods.OpSegment, "branch")) != 1 {
		t.Fatalf("loading the mods started %v", calls)
	}
	blocks, rebuilds := r.m.chat.win.Stats().Rendered, r.m.chat.rebuilds
	r.send(calls[0])
	r.wantBar("answered", defaultLeft, "⎇ main │ 12:00 ")
	// The bar alone is drawn again for it.
	if got := r.m.chat.win.Stats().Rendered; got != blocks || r.m.chat.rebuilds != rebuilds {
		t.Errorf("a segment's answer drew %d blocks and rebuilt the list %d times", got-blocks, r.m.chat.rebuilds-rebuilds)
	}
	// What the mod was told: the terminal, the width, and of the grants it
	// holds, the one thing it asked for.
	in := host.sent("git", mods.OpSegment, "branch")[0].in
	if want := (wire.Term{Color: "truecolor", Glyphs: "unicode", Background: "dark"}); in.Term != want || in.Width != 100 {
		t.Errorf("the mod was told the terminal is %+v, %d wide", in.Term, in.Width)
	}
	if in.Data.Workspace == nil || in.Data.Workspace.Branch != "main" {
		t.Errorf("the mod was told the workspace is %+v", in.Data.Workspace)
	}
	if in.Data.Contact != nil || in.Data.Route != nil || in.Data.State != nil || in.Data.ModelCalls != nil ||
		in.Data.Messages != nil || in.Data.Now != 0 {
		t.Errorf("the mod was told more than it may read: %+v", in.Data)
	}

	// On a narrower terminal the segment shows its short form.
	r.size(60, 30)
	r.wantBar("at 60 columns", " ● live │ fake-1 │ cache 75% │ 1.2s", "main │ 12:00 ")
	r.size(100, 30)

	// The bar follows the glyph set: the mod is asked again, and what it
	// said before is folded meanwhile.
	r.m.over.Glyphs = "ascii"
	r.kept = append(r.kept, r.m.resolve())
	r.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	r.wantBar("ASCII, not asked again yet", " * live | fake-1 | cache 75% | 2.0k tok | 1.2s", "? main | 12:00 ")
	r.settle()
	r.wantBar("ASCII", " * live | fake-1 | cache 75% | 2.0k tok | 1.2s", "main | 12:00 ")
	if in := host.sent("git", mods.OpSegment, "branch")[1].in; in.Term.Glyphs != "ascii" {
		t.Errorf("the mod was told the glyphs are %q", in.Term.Glyphs)
	}

	// Leaving the conversation asks nothing, and another one starts with
	// nothing to show until its own answer is there.
	asked := host.count()
	r.send(ctrl('p'))
	r.settle()
	if host.count() != asked {
		t.Errorf("with no conversation open the mods were called %d times", host.count()-asked)
	}
	r.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	next := r.b.sessions[len(r.b.sessions)-1]
	close(next.updates)
	r.from(next, live(), history(t))
	r.wantBar("opened again", " * live | fake-1 | cache 75% | 2.0k tok | 1.2s", "12:00 ")
	r.settle()
	r.wantBar("opened again, answered", " * live | fake-1 | cache 75% | 2.0k tok | 1.2s", "main | 12:00 ")
	if sess == next || host.count() != asked+1 {
		t.Errorf("opening a conversation again called the mods %d times", host.count()-asked)
	}
}

// Each segment is asked again on what its manifest names: a new record of a
// model call, a turn that ended, another state of the connection or of the
// reply, or an interval.
func TestAModSegmentIsAskedAgainOnItsTrigger(t *testing.T) {
	t.Parallel()
	pulse := mods.Manifest{
		Name: "pulse", Read: []mods.Grant{mods.GrantState, mods.GrantClock},
		Segments: []mods.SegmentDecl{{Name: "link", Refresh: mods.RefreshState}, {Name: "beat", Refresh: "30s"}},
	}
	host := newFakeMods(cacheMod(), gitMod(), pulse)
	host.answers("cache", mods.OpSegment, "cache", cacheAnswer)
	host.answers("git", mods.OpSegment, "branch", branchAnswer)
	host.answers("pulse", mods.OpSegment, "link", func(in mods.Input) (wire.Reply, error) {
		text := in.Data.State.Conn
		if in.Data.State.Replying {
			text += "+"
		}
		return wire.Reply{ABI: wire.ABI, Text: text}, nil
	})
	host.answers("pulse", mods.OpSegment, "beat", func(in mods.Input) (wire.Reply, error) {
		return wire.Reply{ABI: wire.ABI, Text: time.UnixMilli(in.Data.Now).UTC().Format("15:04:05")}, nil
	})
	dir := repo(t, "main")
	r := newModRig(t, trueDark(), host, Options{WorkDir: dir})
	r.size(140, 30)
	r.load()
	sess := r.talk()
	asked := func() [4]int {
		return [4]int{
			len(host.sent("cache", mods.OpSegment, "cache")), len(host.sent("git", mods.OpSegment, "branch")),
			len(host.sent("pulse", mods.OpSegment, "link")), len(host.sent("pulse", mods.OpSegment, "beat")),
		}
	}
	want := func(what string, n [4]int, right string) {
		t.Helper()
		if got := asked(); got != n {
			t.Errorf("%s: cache, branch, link and beat were asked %v times, want %v", what, got, n)
		}
		if bar := r.bar(); !strings.HasSuffix(bar, right) {
			t.Errorf("%s: the bar is\n%q, and should end\n%q", what, bar, right)
		}
	}
	want("opened", [4]int{1, 1, 1, 1}, "calls 1 │ ⎇ main │ live │ 12:00:00 │ 12:00 ")
	// One timer, for the segment on an interval.
	if len(r.timers) != 1 || r.timers[0].wait != 30*time.Second {
		t.Fatalf("the timers are %+v", r.timers)
	}

	// A key, a message that says nothing new: nobody is asked.
	r.typeText("hm")
	r.from(sess, live())
	r.settle()
	want("nothing happened", [4]int{1, 1, 1, 1}, "calls 1 │ ⎇ main │ live │ 12:00:00 │ 12:00 ")

	// A new model call.
	r.from(sess, attempt(t, 6))
	r.settle()
	want("a model call", [4]int{2, 1, 1, 1}, "calls 2 │ ⎇ main │ live │ 12:00:00 │ 12:00 ")

	// A reply begins: the state changed, and no turn has ended.
	r.from(sess, typing(true))
	r.settle()
	want("a reply begins", [4]int{2, 1, 2, 1}, "replying │ esc stop │ calls 2 │ ⎇ main │ live+ │ 12:00:00 │ 12:00 ")

	// The turn ends, with another branch checked out under it: the state
	// changed again, the branch is read again, and the segment that shows it
	// is asked until it has the new one.
	checkout(t, dir, "fix/bar")
	r.from(sess, typing(false))
	r.settle()
	want("the turn ended", [4]int{2, 3, 3, 1}, "calls 2 │ ⎇ fix/bar │ live │ 12:00:00 │ 12:00 ")

	// A turn that begins and ends in one batch still ended.
	r.from(sess, typing(true), typing(false))
	r.settle()
	want("a turn in one batch", [4]int{2, 4, 3, 1}, "calls 2 │ ⎇ fix/bar │ live │ 12:00:00 │ 12:00 ")

	// A lost connection is another state.
	r.from(sess, dropped())
	r.settle()
	want("the connection dropped", [4]int{2, 4, 4, 1}, "calls 2 │ ⎇ fix/bar │ down │ 12:00:00 │ 12:00 ")
	r.from(sess, live())
	r.settle()
	want("connected again", [4]int{2, 4, 5, 1}, "calls 2 │ ⎇ fix/bar │ live │ 12:00:00 │ 12:00 ")

	// The interval: before it has passed the timer's tick asks nobody, and a
	// new timer is set for the rest.
	tick := r.timers[len(r.timers)-1].msg
	r.now = r.now.Add(10 * time.Second)
	r.send(tick)
	r.settle()
	want("ten seconds on", [4]int{2, 4, 5, 1}, "calls 2 │ ⎇ fix/bar │ live │ 12:00:00 │ 12:00 ")
	if last := r.timers[len(r.timers)-1]; len(r.timers) != 2 || last.wait != 20*time.Second {
		t.Fatalf("after an early tick the timers are %+v", r.timers)
	}
	r.now = r.now.Add(20 * time.Second)
	r.send(r.timers[1].msg)
	r.settle()
	want("the interval has passed", [4]int{2, 4, 5, 2}, "calls 2 │ ⎇ fix/bar │ live │ 12:00:30 │ 12:00 ")
	if last := r.timers[len(r.timers)-1]; len(r.timers) != 3 || last.wait != 30*time.Second {
		t.Fatalf("after the interval the timers are %+v", r.timers)
	}
	// The tick of a timer that was replaced sets no other.
	r.send(tick)
	r.settle()
	if got := asked(); got != [4]int{2, 4, 5, 2} || len(r.timers) != 3 {
		t.Errorf("an old timer's tick asked %v and left the timers %+v", got, r.timers)
	}

	// With the conversation left, the timer's tick asks nobody.
	r.send(ctrl('c'))
	r.send(ctrl('p'))
	r.now = r.now.Add(time.Minute)
	r.send(r.timers[len(r.timers)-1].msg)
	r.settle()
	if got := asked(); got != [4]int{2, 4, 5, 2} {
		t.Errorf("with the conversation left the segments were asked %v times", got)
	}
}

// One call for a segment is on its way at a time. What happens meanwhile is
// remembered, and the segment is asked once more when the answer is back,
// with the conversation as it is then.
func TestATriggerDuringACallAsksOnceMore(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod())
	host.answers("cache", mods.OpSegment, "cache", cacheAnswer)
	r := newModRig(t, trueDark(), host, Options{WorkDir: repo(t, "main")})
	r.load()
	r.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	sess := r.b.sessions[0]
	close(sess.updates)
	r.from(sess, live(), history(t))
	first := r.started() // the call is made, and its answer is not back
	sent := func() int { return len(host.sent("cache", mods.OpSegment, "cache")) }
	if sent() != 1 {
		t.Fatalf("opening the conversation called the segment %d times", sent())
	}
	// Three model calls while the answer is on its way: no second call.
	for seq := uint64(6); seq <= 8; seq++ {
		r.from(sess, attempt(t, seq))
		if msgs := r.started(); sent() != 1 {
			t.Fatalf("a model call while the segment was being asked called it again: %v", msgs)
		}
	}
	r.wantBar("no answer yet", defaultLeft[:len(" ● live │ fake-1 │ ")]+"cache 50% │ 1.0k tok │ 0.9s", defaultRight)

	// The first answer is back: it is shown, and the segment is asked once
	// more, once, with all four calls.
	var answers []tea.Msg
	for _, msg := range first {
		if _, ok := msg.(modSegmentMsg); ok {
			answers = append(answers, msg)
		}
	}
	if len(answers) != 1 {
		t.Fatalf("opening the conversation started %v", first)
	}
	r.send(answers[0])
	if got := r.bar(); !strings.HasSuffix(got, "calls 1 │ 12:00 ") {
		t.Errorf("after the first answer the bar is %q", got)
	}
	again := r.started()
	if sent() != 2 || len(again) != 1 {
		t.Fatalf("after the answer the segment was called %d times in all: %v", sent(), again)
	}
	if calls := host.sent("cache", mods.OpSegment, "cache")[1].in.Data.ModelCalls; len(calls) != 4 || calls[3].Seq != 8 {
		t.Errorf("the second call was told of %+v", calls)
	}
	r.send(again[0])
	r.settle()
	if got := r.bar(); sent() != 2 || !strings.HasSuffix(got, "calls 4 │ 12:00 ") {
		t.Errorf("at rest the segment was called %d times and the bar is %q", sent(), got)
	}
	// The records a mod was handed are not changed under it.
	if calls := host.sent("cache", mods.OpSegment, "cache")[0].in.Data.ModelCalls; len(calls) != 1 || calls[0].Seq != 4 {
		t.Errorf("the first call's records are now %+v", calls)
	}
	// No mod here may know where the client started, so a turn that ends
	// has nothing read for it.
	r.from(sess, typing(true), typing(false))
	for _, msg := range r.started() {
		if _, read := msg.(workspaceMsg); read {
			t.Error("the workspace was read with no mod that may know it")
		}
	}
}

// An answer that comes back after its conversation was left for another says
// nothing in the one that is open: a segment's value is not shown and the
// segment is asked about the new contact, and a command's lines are dropped.
func TestAnswersAboutAConversationThatWasLeft(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod())
	host.answers("cache", mods.OpSegment, "cache", cacheAnswer)
	host.says("cache", mods.OpCommand, "stats", wire.Reply{
		ABI: wire.ABI, Lines: []wire.Line{{{T: "ada's numbers"}}}, Insert: "about ada", Note: "counted ada",
	})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	ada := r.talk()
	r.from(ada, attempt(t, 6))
	r.command("/stats")
	late := r.started() // both are asked about ada, and neither has answered
	if got := host.count(); got != 3 {
		t.Fatalf("the mods were called %d times", got)
	}

	r.m.open(client.Contact{ID: 2, Name: "bob", Provider: "fake", Model: "fake-1"})
	bob := r.b.sessions[len(r.b.sessions)-1]
	close(bob.updates)
	r.from(bob, live())
	// The segment is still being asked about ada: it is not asked twice.
	if msgs := r.started(); host.count() != 3 {
		t.Fatalf("a segment with a call on its way was called again: %v", msgs)
	}
	r.wantBar("bob, nothing known", " ● live │ fake-1", defaultRight)
	for _, msg := range late {
		r.send(msg)
	}
	r.wantBar("bob, ada's answers back", " ● live │ fake-1", defaultRight)
	r.wantNotOnScreen("ada's numbers", "calls 2")
	if r.draft() != "" || r.note() != "" {
		t.Errorf("ada's answer left %q in bob's box and said %q", r.draft(), r.note())
	}
	// Now the segment is asked about bob, who has no model call yet.
	r.settle()
	r.wantBar("bob, answered", " ● live │ fake-1", "calls 0 │ 12:00 ")
	if got := host.sent("cache", mods.OpSegment, "cache"); len(got) != 3 || len(got[2].in.Data.ModelCalls) != 0 {
		t.Errorf("the segment was asked %d times, last about %+v", len(got), got[len(got)-1].in.Data.ModelCalls)
	}
}

// A note for the conversation that arrives while the /mods screen is over it
// is there when the screen is left.
func TestANoteUnderTheModsScreen(t *testing.T) {
	t.Parallel()
	r := newModRig(t, trueDark(), newFakeMods(cacheMod()), Options{})
	r.load()
	r.talk()
	r.b.err = errors.New("the server said no")
	r.command("/rename grace") // asked, and not answered before the screen opens
	r.command("/mods")
	r.settle()
	if r.m.screen != screenMods || r.m.note != "" {
		t.Fatalf("the answer left the screen %v and the list's note %q", r.m.screen, r.m.note)
	}
	r.send(esc())
	if r.note() != "the server said no" {
		t.Errorf("back in the conversation the note is %q", r.note())
	}
	r.wantOnScreen("the server said no")
}

// The /mods screen hears of a new width when it happens, not when it is next
// drawn: a key that follows a resize means what it means at the new width.
func TestTheModsScreenHearsOfAResize(t *testing.T) {
	t.Parallel()
	r := newModRig(t, trueDark(), newFakeMods(cacheMod(), gitMod()), Options{})
	r.load()
	r.talk()
	r.command("/mods")
	r.settle()
	r.wantOnScreen("A MOD CAN ADD", "MAY READ")
	// Enter does nothing on a wide screen, and shows the details in place of
	// the list on a narrow one.
	r.send(enter())
	r.wantOnScreen("A MOD CAN ADD", "MAY READ")
	r.size(60, 16)
	r.send(enter())
	r.wantOnScreen("MAY READ")
	r.wantNotOnScreen("A MOD CAN ADD")
	r.wantFits("the details", 60, 16)
	// Esc brings the list back, and then leaves.
	r.send(esc())
	if r.m.screen != screenMods {
		t.Fatal("esc on the details left the screen")
	}
	r.wantOnScreen("A MOD CAN ADD")
	r.send(esc())
	if r.m.screen != screenChat {
		t.Fatal("esc on the list did not leave the screen")
	}
}

// What a mod's command answers: lines are a block of the conversation under
// the mod's name, text goes into the box and is never sent, and a note goes
// where the key hints are.
func TestAModCommand(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod(), standupMod())
	host.answers("cache", mods.OpCommand, "stats", func(in mods.Input) (wire.Reply, error) {
		switch {
		case len(in.Args) > 1:
			return wire.Reply{ABI: wire.ABI, Error: "usage: /stats [days]"}, nil
		case len(in.Args) == 1 && in.Args[0] == "0":
			return wire.Reply{ABI: wire.ABI, Note: "no model calls to count yet"}, nil
		case len(in.Args) == 1 && in.Args[0] == "9":
			return wire.Reply{}, errors.New("mod cache: stopped at the deadline")
		}
		return wire.Reply{ABI: wire.ABI, Lines: []wire.Line{
			{{T: "Mon 12 Oct", Tone: "dim"}, {T: "  1 call  2.0k in  75%"}},
			{{T: "\x1b]0;owned\x07\x1b[2Jtotal 75%", Bold: true}},
		}}, nil
	})
	host.says("standup", mods.OpCommand, "standup", wire.Reply{
		ABI: wire.ABI, Insert: "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked.",
		Note: "edit it, then enter sends it", Tone: "ok",
	})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	sess := r.talk()

	// The commands are listed with the core ones, each with its mod.
	r.typeText("/sta")
	r.wantOnScreen("/stats  ", "token use and cache share by day", "mod cache", "/standup", "mod standup")
	r.send(ctrl('c'))

	// Lines.
	items := len(r.m.chat.tr.Items())
	r.command("/stats")
	if r.draft() != "" || host.count() != 1 {
		t.Fatalf("before the command is run the draft is %q and the host was called %d times", r.draft(), host.count())
	}
	r.settle()
	// What would start an escape sequence is dropped; the rest is text.
	r.wantOnScreen("\ncache\n", "  Mon 12 Oct  1 call  2.0k in  75%", "  ]0;owned[2Jtotal 75%")
	if raw := r.m.View().Content; strings.ContainsAny(raw, "\x07") || strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\x1b[2J") {
		t.Errorf("a mod's escape sequences reached the screen:\n%q", raw)
	}
	call := host.sent("cache", mods.OpCommand, "stats")[0].in
	if len(call.Args) != 0 || call.Width != 98 || len(call.Data.ModelCalls) != 1 {
		t.Errorf("the command was sent %+v", call)
	}
	// It is the client's own: nothing of it is in the conversation's log,
	// and nothing was said to the contact.
	if got := len(r.m.chat.tr.Items()); got != items || len(sess.said) != 0 || sess.typings != 0 {
		t.Errorf("after /stats there are %d items of %d, %q was said, and typing was raised %d times", got, items, sess.said, sess.typings)
	}

	// Arguments, a note, and a note in the mod's own tone.
	r.command("/stats 0")
	r.settle()
	if r.note() != "no model calls to count yet" || r.m.chat.noteTone != "" || r.draft() != "" {
		t.Errorf("/stats 0 says %q in the tone %q and leaves %q", r.note(), r.m.chat.noteTone, r.draft())
	}
	if args := host.sent("cache", mods.OpCommand, "stats")[1].in.Args; !slices.Equal(args, []string{"0"}) {
		t.Errorf("/stats 0 was sent the arguments %q", args)
	}

	// An answer that is an error, and a call that failed: the note says so
	// and the line is back in the box, to be put right.
	r.command(`/stats 1 "2 3"`)
	r.settle()
	if r.note() != "usage: /stats [days]" || r.draft() != `/stats 1 "2 3"` {
		t.Errorf("a refused command says %q and leaves %q", r.note(), r.draft())
	}
	if args := host.sent("cache", mods.OpCommand, "stats")[2].in.Args; !slices.Equal(args, []string{"1", "2 3"}) {
		t.Errorf("the arguments were sent as %q", args)
	}
	r.send(ctrl('c'))
	r.command("/stats 9")
	r.settle()
	if r.note() != "mod cache: stopped at the deadline" || r.draft() != "/stats 9" {
		t.Errorf("a failed command says %q and leaves %q", r.note(), r.draft())
	}
	r.send(ctrl('c'))

	// Text for the box. It is there to be edited: nothing is sent, and the
	// contact is not told that someone is writing.
	r.command("/standup")
	r.settle()
	const inserted = "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked."
	if r.draft() != inserted || len(sess.said) != 0 || sess.typings != 0 {
		t.Errorf("/standup left %q, said %q and raised typing %d times", r.draft(), sess.said, sess.typings)
	}
	if r.note() != "edit it, then enter sends it" || r.m.chat.noteTone != "ok" {
		t.Errorf("/standup says %q in the tone %q", r.note(), r.m.chat.noteTone)
	}
	if in := host.sent("standup", mods.OpCommand, "standup")[0].in; in.Data.Now != fixedNow().UnixMilli() || in.Data.ModelCalls != nil {
		t.Errorf("/standup was told %+v", in.Data)
	}
	// Enter is the person's own, and sends it as any message.
	r.send(enter())
	if !slices.Equal(sess.said, []string{inserted}) {
		t.Errorf("enter on the inserted text said %q", sess.said)
	}

	// Text that arrives while something else was typed stands before it.
	r.typeText("/standup")
	r.send(enter())
	r.typeText("and thank them")
	r.settle()
	if want := inserted + "\nand thank them"; r.draft() != want {
		t.Errorf("text inserted beside a draft gives %q", r.draft())
	}

	// An answer for a conversation that was left is dropped.
	r.send(ctrl('c'))
	r.command("/stats")
	answers := r.started()
	r.send(ctrl('p'))
	for _, msg := range answers {
		r.send(msg)
	}
	if r.m.chat != nil || r.m.note != "" {
		t.Errorf("a late answer opened %v or said %q", r.m.chat, r.m.note)
	}
}

// A mod is sent the conversation's messages only when its manifest holds
// that grant, and then only for its command. A mod is sent nothing its
// manifest does not ask for.
func TestWhatAModIsSent(t *testing.T) {
	t.Parallel()
	reader := mods.Manifest{
		Name: "reader", Read: mods.Grants(),
		Segments: []mods.SegmentDecl{{Name: "seen", Refresh: mods.RefreshModelCall}},
		Commands: []mods.CommandDecl{{Name: "recap"}},
	}
	blind := mods.Manifest{
		Name:     "blind",
		Segments: []mods.SegmentDecl{{Name: "dot", Refresh: mods.RefreshModelCall}},
		Commands: []mods.CommandDecl{{Name: "poke"}},
	}
	// It may read everything but the messages.
	most := mods.Manifest{
		Name: "most", Read: slices.DeleteFunc(mods.Grants(), func(g mods.Grant) bool { return g == mods.GrantMessages }),
		Commands: []mods.CommandDecl{{Name: "most"}},
	}
	dir := repo(t, "main")
	for name, tc := range map[string]struct {
		installed []mods.Manifest
		// messages says whether /recap, when there is one, is sent them.
		recap bool
	}{
		"a mod with the grant beside mods without": {installed: []mods.Manifest{reader, blind, most}, recap: true},
		"no mod with the grant":                    {installed: []mods.Manifest{blind, most}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			host := newFakeMods(tc.installed...)
			r := newModRig(t, trueDark(), host, Options{WorkDir: dir})
			r.load()
			sess := r.talk()
			r.from(sess, typing(true))
			for _, line := range []string{"/recap", "/poke", "/most"} {
				r.settle()
				r.send(ctrl('c'))
				r.command(line)
			}
			r.settle()
			host.mu.Lock()
			calls := slices.Clone(host.calls)
			host.mu.Unlock()
			var recaps int
			for _, c := range calls {
				d := c.in.Data
				switch {
				case c.mod == "reader" && c.op == mods.OpCommand:
					recaps++
					want := []wire.Message{
						{Seq: 1, TS: fixedNow().Add(-time.Hour).UnixMilli(), Role: "user", Text: "what is 6*7?"},
						{Seq: 5, TS: fixedNow().Add(-time.Hour).UnixMilli(), Role: "agent", Text: "It is 42."},
					}
					if !slices.Equal(d.Messages, want) {
						t.Errorf("/recap was sent the messages %+v", d.Messages)
					}
				case d.Messages != nil:
					t.Errorf("%s %s %s was sent messages: %+v", c.mod, c.op, c.name, d.Messages)
				}
				switch c.mod {
				case "blind":
					if d.Contact != nil || d.Route != nil || d.State != nil || d.ModelCalls != nil || d.Workspace != nil || d.Now != 0 {
						t.Errorf("a mod with no grant was sent %+v", d)
					}
				case "reader", "most":
					if d.Contact == nil || *d.Contact != (wire.Contact{ID: 1, Name: "ada"}) ||
						d.Route == nil || *d.Route != (wire.Route{Provider: "fake", Model: "fake-1"}) ||
						d.Workspace == nil || d.Workspace.Branch != "main" || d.Workspace.Dir != filepath.Base(dir) ||
						d.Now != fixedNow().UnixMilli() || len(d.ModelCalls) != 1 {
						t.Errorf("%s %s was sent %+v", c.mod, c.op, d)
					}
					if c.op == mods.OpCommand && (d.State == nil || *d.State != (wire.State{Conn: statusline.ConnLive, Replying: true})) {
						t.Errorf("%s %s was sent the state %+v", c.mod, c.op, d.State)
					}
				}
			}
			if (recaps == 1) != tc.recap {
				t.Errorf("/recap reached its mod %d times", recaps)
			}
			if want := (wire.ModelCall{Seq: 4, TS: fixedNow().Add(-time.Hour).UnixMilli(), Route: "fake/fake-1", Input: 2000, Cached: 1500, Output: 90, MS: 1234}); tc.recap {
				if got := host.sent("reader", mods.OpSegment, "seen")[0].in.Data.ModelCalls[0]; got != want {
					t.Errorf("a model call was sent as %+v", got)
				}
			}
		})
	}
}

// The messages a command is sent are the newest twenty, oldest first.
func TestAModIsSentTheNewestMessages(t *testing.T) {
	t.Parallel()
	host := newFakeMods(mods.Manifest{Name: "reader", Read: []mods.Grant{mods.GrantMessages}, Commands: []mods.CommandDecl{{Name: "recap"}}})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	sess := r.talk()
	r.from(sess, messages(t, 10, 60, fixedNow()))
	r.command("/recap")
	r.settle()
	got := host.sent("reader", mods.OpCommand, "recap")[0].in.Data.Messages
	if len(got) != 20 || got[0].Seq != 41 || got[19].Seq != 60 {
		t.Fatalf("the command was sent %d messages: %+v", len(got), got)
	}
}

// A mod that ran over three times in a row is switched off by the host. The
// client says so in the conversation, once, and what the mod added is gone:
// its commands and its segment.
func TestAModThatIsSwitchedOff(t *testing.T) {
	t.Parallel()
	slow := mods.Manifest{
		Name: "slow", Read: []mods.Grant{mods.GrantModelCalls},
		Segments: []mods.SegmentDecl{{Name: "lag", Refresh: mods.RefreshModelCall}},
		Commands: []mods.CommandDecl{{Name: "lag", Summary: "how far behind"}},
	}
	host := newFakeMods(slow, gitMod())
	host.answers("git", mods.OpSegment, "branch", branchAnswer)
	strikes := 0
	host.answers("slow", mods.OpSegment, "lag", func(mods.Input) (wire.Reply, error) {
		strikes++
		switch strikes {
		case 1:
			return wire.Reply{ABI: wire.ABI, Text: "lag 2s"}, nil
		case 2, 3:
			return wire.Reply{}, errors.New("mod slow: stopped at the deadline")
		}
		host.set("slow", mods.StatusDisabled, "over budget 3 times in a row, 2026-10-12 12:00")
		return wire.Reply{}, fmt.Errorf("mod slow: %w: over budget 3 times in a row", mods.ErrDisabled)
	})
	r := newModRig(t, trueDark(), host, Options{WorkDir: repo(t, "main")})
	r.load()
	sess := r.talk()
	r.wantBar("the first answer", defaultLeft, "⎇ main │ lag 2s │ 12:00 ")
	if _, ok := r.m.cmds.Lookup("lag"); !ok {
		t.Fatal("the mod's command is not registered")
	}

	// Two calls that fail: the segment says it does not know.
	for seq := uint64(6); seq <= 7; seq++ {
		r.from(sess, attempt(t, seq))
		r.settle()
		r.wantBar("a failed call", " ● live │ fake-1 │ cache 50% │ 1.0k tok │ 0.9s", "⎇ main │ lag ? │ 12:00 ")
		r.wantNotOnScreen("switched off")
	}
	// The third strike.
	r.typeText("/la")
	r.wantOnScreen("how far behind")
	r.from(sess, attempt(t, 8))
	r.settle()
	r.wantOnScreen("\n  mod slow was switched off: over budget 3 times in a row\n")
	r.wantBar("switched off", " ● live │ fake-1 │ cache 50% │ 1.0k tok │ 0.9s", "⎇ main │ 12:00 ")
	if _, ok := r.m.cmds.Lookup("lag"); ok {
		t.Error("the command of a mod that was switched off is still registered")
	}
	// The list of commands, which was open on it, no longer offers it.
	r.wantNotOnScreen("how far behind")
	if len(r.m.modSegs) != 1 || r.m.modSegs[0] != (statusline.ModSegment{Mod: "git", Name: "branch"}) {
		t.Errorf("the segments offered are %+v", r.m.modSegs)
	}
	// It is not called again, and nothing is said twice.
	calls := host.count()
	r.send(ctrl('c'))
	r.from(sess, attempt(t, 9))
	r.settle()
	if got := len(host.sent("slow", mods.OpSegment, "lag")); got != 4 || host.count() != calls {
		t.Errorf("a mod that is off was called: %d calls of its segment, %d more in all", got, host.count()-calls)
	}
	if got := strings.Count(r.screen(), "was switched off"); got != 1 {
		t.Errorf("the notice is on the screen %d times:\n%s", got, r.screen())
	}
	// Its command is no command any more.
	r.command("/lag")
	if !strings.HasPrefix(r.note(), "no command /lag") {
		t.Errorf("/lag says %q", r.note())
	}
	// The other mod goes on.
	r.send(ctrl('c'))
	r.from(sess, typing(true), typing(false))
	r.settle()
	if got := len(host.sent("git", mods.OpSegment, "branch")); got != 2 {
		t.Errorf("the mod that is still on was asked %d times", got)
	}
	// /status says why.
	r.size(100, 60)
	r.command("/status")
	r.wantOnScreen("  mod git      on, 0.1.0: segment branch", "  mod slow     disabled: over budget 3 times in a row, 2026-10-12 12:00")
}

// The switch-off is said whichever call it came from, and with no reason
// when the host gives none.
func TestACommandThatSwitchesItsModOff(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod())
	host.answers("cache", mods.OpCommand, "stats", func(mods.Input) (wire.Reply, error) {
		return wire.Reply{}, fmt.Errorf("mod cache: %w", mods.ErrDisabled)
	})
	host.says("cache", mods.OpSegment, "cache", wire.Reply{ABI: wire.ABI, Text: "cache avg 75%"})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	r.talk()
	r.wantBar("on", defaultLeft, "cache avg 75% │ 12:00 ")
	r.command("/stats")
	r.settle()
	// The fake host still lists the mod as on; the client does not.
	r.wantOnScreen("\n  mod cache was switched off\n")
	r.wantBar("off", defaultLeft, defaultRight)
	if _, ok := r.m.cmds.Lookup("stats"); ok || r.note() != "" {
		t.Errorf("after the switch-off /stats is still there, or the note is %q", r.note())
	}
}

// A mod that draws a kind of item is asked for each such item in view, in a
// command. Until it answers the client draws the item itself; then the block
// is drawn again from the mod's lines. What it drew is kept by item, part and
// width.
func TestAModDrawsAnItem(t *testing.T) {
	t.Parallel()
	chart := mods.Manifest{
		Name: "chart", Read: []mods.Grant{mods.GrantMessages},
		Renderers: []mods.RendererDecl{{Kind: "code:Mermaid"}, {Kind: "tool:python"}},
	}
	host := newFakeMods(chart)
	host.answers("chart", mods.OpRender, "code:Mermaid", func(in mods.Input) (wire.Reply, error) {
		if strings.Contains(in.Item.Text, "pass") {
			// A mod that passes is not drawn from, whatever else it sends.
			return wire.Reply{ABI: wire.ABI, Pass: true, Lines: []wire.Line{{{T: "not to be drawn"}}}}, nil
		}
		return wire.Reply{ABI: wire.ABI, Lines: []wire.Line{
			{{T: "┌─ drawn at " + strconv.Itoa(in.Width) + " ─┐", Tone: "mod"}},
			{{T: "\x1b[2J" + strings.ToUpper(in.Item.Text)}},
		}}, nil
	})
	host.says("chart", mods.OpRender, "tool:python", wire.Reply{ABI: wire.ABI, Lines: []wire.Line{{{T: "a table of nine rows"}}}})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	sess := r.talk()
	r.settle()
	// The tool's output of the history is the mod's to draw.
	r.wantOnScreen("⎿ a table of nine rows")
	r.wantNotOnScreen("row 1", "more rows")
	if got := host.sent("chart", mods.OpRender, "tool:python"); len(got) != 1 || got[0].in.Item == nil ||
		got[0].in.Item.Kind != "tool:python" || !strings.HasPrefix(got[0].in.Item.Text, "row 1\nrow 2") || got[0].in.Data.Messages != nil {
		t.Fatalf("the tool's output was asked for as %+v", got)
	}

	// A reply with two diagrams and a block in a language nobody draws.
	const reply = "Two ways:\n\n```mermaid\ngraph a\n```\n\nor\n\n```MERMAID\npass b\n```\n\n```go\nfmt.Println()\n```\n"
	at := fixedNow()
	r.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 6, at, core.KindAgentMsg, core.MsgBody{Text: reply}),
	}})
	// Asked, not answered: the client's own drawing.
	r.wantOnScreen("graph a", "pass b", "fmt.Println()")
	blocks := r.m.chat.win.Stats().Rendered
	asks := r.started()
	var drawn []modRenderedMsg
	for _, msg := range asks {
		if d, ok := msg.(modRenderedMsg); ok {
			drawn = append(drawn, d)
		}
	}
	if len(drawn) != 2 || len(host.sent("chart", mods.OpRender, "code:Mermaid")) != 2 {
		t.Fatalf("the reply's blocks were asked for as %v", asks)
	}
	first := host.sent("chart", mods.OpRender, "code:Mermaid")[0].in
	if first.Item.Kind != "code:Mermaid" || first.Item.Text != "graph a" || first.Width != 98 || first.Term.Glyphs != "unicode" {
		t.Errorf("the block was asked for as %+v at %d", first.Item, first.Width)
	}
	// Drawing the screen again asks nobody again.
	r.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	r.m.chat.dirty |= dirtyBody
	r.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	if again := r.started(); len(again) != 0 {
		t.Fatalf("a block that was asked for was asked for again: %v", again)
	}
	for _, d := range drawn {
		r.send(d)
	}
	// The one block is drawn again, with the mod's lines for the diagram it
	// drew and the client's for the one it passed.
	r.wantOnScreen("┌─ drawn at 98 ─┐", "GRAPH A", "pass b", "fmt.Println()")
	r.wantNotOnScreen("graph a", "PASS B", "not to be drawn")
	if got := r.m.chat.win.Stats().Rendered - blocks; got != 1 {
		t.Errorf("a mod's answer drew %d blocks", got)
	}
	if raw := r.m.View().Content; strings.Contains(raw, "\x1b[2J") {
		t.Errorf("a mod's escape sequence reached the screen:\n%q", raw)
	}
	r.settle()
	if got := len(host.sent("chart", mods.OpRender, "code:Mermaid")); got != 2 {
		t.Errorf("the mod was asked %d times for two blocks", got)
	}

	// Folding and scrolling ask nobody: the drawing is kept.
	r.send(ctrl('o'))
	r.send(special(tea.KeyPgUp))
	r.send(special(tea.KeyPgDown))
	r.settle()
	r.wantOnScreen("GRAPH A")
	if got := host.count(); got != 3 {
		t.Errorf("after folding and scrolling the mod was asked %d times", got)
	}
	// Another width is another drawing; the old width's is kept.
	r.size(80, 30)
	r.wantOnScreen("graph a")
	r.settle()
	r.wantOnScreen("┌─ drawn at 78 ─┐", "GRAPH A")
	asked := host.count()
	r.size(100, 30)
	r.settle()
	r.wantOnScreen("┌─ drawn at 98 ─┐")
	if host.count() != asked {
		t.Errorf("going back to a width asked the mod %d times", host.count()-asked)
	}

	// With the mod switched off the client draws the items itself again.
	r.command("/mods off chart")
	r.settle()
	r.wantOnScreen("graph a", "pass b")
	r.wantNotOnScreen("GRAPH A", "drawn at")
	asked = host.count()
	r.send(ctrl('o'))
	r.settle()
	if host.count() != asked {
		t.Errorf("a mod that is off was asked to draw %d times", host.count()-asked)
	}
}

// What a mod drew for one terminal is not kept for another: the mod is told
// what the terminal can do.
func TestAModDrawsAgainForAnotherTerminal(t *testing.T) {
	t.Parallel()
	chart := mods.Manifest{Name: "chart", Read: []mods.Grant{mods.GrantMessages}, Renderers: []mods.RendererDecl{{Kind: "tool:python"}}}
	host := newFakeMods(chart)
	host.answers("chart", mods.OpRender, "tool:python", func(in mods.Input) (wire.Reply, error) {
		return wire.Reply{ABI: wire.ABI, Lines: []wire.Line{{{T: "drawn for " + in.Term.Glyphs + " on " + in.Term.Background}}}}, nil
	})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	sess := r.talk()
	r.settle()
	r.wantOnScreen("drawn for unicode on dark")
	r.command("/glyphs ascii")
	r.wantOnScreen("row 1")
	r.settle()
	r.wantOnScreen("drawn for ascii on dark")
	r.wantNotOnScreen("drawn for unicode")
	// A failed call leaves the client's drawing, and is not made again for
	// every time the screen is drawn.
	host.answers("chart", mods.OpRender, "tool:python", func(mods.Input) (wire.Reply, error) {
		return wire.Reply{}, errors.New("mod chart: stopped at the deadline")
	})
	asked := host.count()
	r.command("/theme light")
	r.settle()
	r.send(ctrl('o'))
	r.send(ctrl('o'))
	r.settle()
	r.wantOnScreen("row 1")
	if got := host.count() - asked; got != 1 {
		t.Errorf("a drawing that failed was asked for %d times", got)
	}

	// An item too long to hand to a mod is the client's to draw: the mod is
	// not asked.
	asked = host.count()
	r.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 6, fixedNow(), core.KindToolCall, core.ToolCall{ID: "c2", Name: "python", Code: "print(big)"}),
		logged(t, 7, fixedNow(), core.KindToolResult, core.ToolResult{ID: "c2", Output: strings.Repeat("nine thousand\n", 700)}),
	}})
	r.settle()
	r.wantOnScreen("|_ nine thousand", "696 more rows")
	if host.count() != asked {
		t.Errorf("a tool's output of 9 KiB was handed to the mod %d times", host.count()-asked)
	}
}

// /mods opens a screen over the conversation that lists the mods. What is
// asked for there is done in a command, and the list is the host's own
// afterwards. Esc returns to the conversation.
func TestTheModsScreen(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod(), gitMod())
	host.set("git", mods.StatusOff, "")
	host.answers("git", mods.OpSegment, "branch", branchAnswer)
	host.says("cache", mods.OpSegment, "cache", wire.Reply{ABI: wire.ABI, Text: "cache avg 75%"})
	work := repo(t, "main")
	host.dist[filepath.Join(work, "dist", "mods", "standup")] = standupMod()
	r := newModRig(t, trueDark(), host, Options{WorkDir: work})
	host.dist[filepath.Join(r.m.opt.Home, "src", "standup")] = standupMod()
	r.load()
	sess := r.talk()
	r.wantBar("one mod on", defaultLeft, "cache avg 75% │ 12:00 ")

	r.command("/mods")
	if r.m.screen != screenMods || r.draft() != "" {
		t.Fatalf("/mods left the screen %v and the draft %q", r.m.screen, r.draft())
	}
	rows := r.rows()
	if want := spread(100, "Mods", "~/contactd/mods · 1 on, 1 off"); rows[0] != want {
		t.Errorf("the header is\n%q, want\n%q", rows[0], want)
	}
	r.wantOnScreen("▸ ", "cache", "0.3.1", "segment · /stats", "git", "0.1.0",
		"How much of each prompt the provider", "MAY READ", "model-call records of the open contact",
		"space on or off · enter details · i install from a path · u update", "esc back")
	if v := r.m.View(); v.WindowTitle != "contactd: mods" || !v.AltScreen || v.Cursor != nil {
		t.Errorf("the screen's view is titled %q, with the cursor %v", v.WindowTitle, v.Cursor)
	}
	// The list is asked for again when the screen opens: the timings change
	// with every call.
	host.mu.Lock()
	host.infos[0].Calls, host.infos[0].Median, host.infos[0].Slowest = 14, 400*time.Microsecond, 1900*time.Microsecond
	host.mu.Unlock()
	r.settle()
	r.wantOnScreen("median 0.4 ms · slowest 1.9 ms")

	// Space on the second mod switches it on. Nothing is done before the
	// command runs.
	r.send(special(tea.KeyDown))
	r.wantOnScreen("The git branch checked out where the", "the directory name and git branch")
	r.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if len(host.done()) != 0 {
		t.Fatalf("the key did %q by itself", host.done())
	}
	r.settle()
	if !slices.Equal(host.done(), []string{"on git"}) {
		t.Fatalf("space did %q", host.done())
	}
	r.wantOnScreen("~/contactd/mods · 2 on, 0 off")
	// Its segment is in the bar under the screen, asked for at once.
	if got := len(host.sent("git", mods.OpSegment, "branch")); got != 1 {
		t.Errorf("the segment of a mod that was switched on was asked %d times", got)
	}
	// Space again switches it off.
	r.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	r.settle()
	r.wantOnScreen("~/contactd/mods · 1 on, 1 off")
	r.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	r.settle()

	// i asks for a path. What is typed there is text, whatever it reads: q
	// does not leave, and the conversation under the screen gets none of it.
	r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if v := r.m.View(); v.Cursor == nil {
		t.Error("the path input has no cursor")
	}
	r.typeText("dist/mods/standup")
	r.wantOnScreen("dist/mods/standup")
	r.send(enter())
	r.settle()
	if want := "install " + filepath.Join(work, "dist", "mods", "standup"); host.done()[len(host.done())-1] != want {
		t.Fatalf("the install did %q, want %q", host.done(), want)
	}
	// The mod that arrived is selected, off, with what it may read.
	r.wantOnScreen("~/contactd/mods · 2 on, 1 off", "standup", "0.2.0", "Drafts a message that asks for", "the time")
	if _, ok := r.m.cmds.Lookup("standup"); ok {
		t.Error("the command of a mod that is off is registered")
	}
	// A path under the home directory, pasted.
	r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
	r.send(tea.PasteMsg{Content: "~/src/standup"})
	r.send(enter())
	r.settle()
	if want := "install " + filepath.Join(r.m.opt.Home, "src", "standup"); host.done()[len(host.done())-1] != want {
		t.Fatalf("the install did %q, want %q", host.done(), want)
	}
	// A path that holds no mod: the screen says so, in place of its keys,
	// until the next key.
	r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
	r.typeText("/nowhere")
	r.send(enter())
	r.settle()
	r.wantOnScreen("install: open /nowhere/mod.toml: no such file or directory")
	r.wantNotOnScreen("esc back")
	r.send(special(tea.KeyUp))
	r.wantOnScreen("esc back")
	r.wantNotOnScreen("no such file")

	// u copies the selected mod again. The host found a newer one, so the
	// mod is off until it is switched on again, and what it added is gone.
	host.changed["git"] = "0.2.0"
	r.wantOnScreen("▸ [x] git")
	r.send(tea.KeyPressMsg{Code: 'u', Text: "u"})
	r.settle()
	if got := host.done(); got[len(got)-1] != "update git" {
		t.Fatalf("u did %q", got)
	}
	r.wantOnScreen("~/contactd/mods · 1 on, 2 off", "git 0.2.0", "updated: switch it on to use the new")
	if len(r.m.modSegs) != 1 || r.m.modSegs[0].Mod != "cache" {
		t.Errorf("after the update the segments offered are %+v", r.m.modSegs)
	}
	// An update that fails says why.
	host.fail["update git"] = errors.New("update: mod git was not installed from a directory")
	r.send(tea.KeyPressMsg{Code: 'u', Text: "u"})
	r.settle()
	r.wantOnScreen("update: mod git was not installed from a directory")

	// The conversation goes on under the screen.
	r.from(sess, client.Update{Kind: client.UpdateEvents, Events: []core.Event{
		logged(t, 6, fixedNow(), core.KindAgentMsg, core.MsgBody{Text: "Still here."}),
	}})
	r.settle()
	r.wantNotOnScreen("Still here.")
	// Two keys are the client's own on this screen too.
	if !clears(r.send(ctrl('l'))) {
		t.Error("ctrl+l did not clear the screen")
	}
	// Esc returns, to the conversation as it is now.
	r.send(esc())
	if r.m.screen != screenChat || r.m.mods.screen != nil {
		t.Fatalf("esc left the screen %v", r.m.screen)
	}
	r.wantOnScreen("Still here.", "❯ Message ada")
	r.wantBar("back", defaultLeft, "cache avg 75% │ 12:00 ")
	if len(sess.said) != 0 || sess.typings != 0 || r.draft() != "" {
		t.Errorf("the screen's keys reached the conversation: said %q, typing %d, draft %q", sess.said, sess.typings, r.draft())
	}
	if got := r.m.View().WindowTitle; got != "contactd: ada" {
		t.Errorf("back in the conversation the title is %q", got)
	}
}

// Ctrl+c leaves the client from the /mods screen, also while a path is being
// typed; text that only reads like the key is text.
func TestTheModsScreensOwnKeys(t *testing.T) {
	t.Parallel()
	for name, typing := range map[string]bool{"on the list": false, "while a path is typed": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			host := newFakeMods(cacheMod())
			r := newModRig(t, trueDark(), host, Options{})
			r.load()
			r.talk()
			r.command("/mods")
			r.settle()
			if typing {
				r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
				r.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, Text: "ctrl+c"})
				r.wantOnScreen("ctrl+c")
			}
			if r.m.screen != screenMods {
				t.Fatalf("the screen is %v", r.m.screen)
			}
			cmd := r.send(ctrl('c'))
			if msgs := produced(cmd); len(msgs) != 1 || msgs[0] != tea.Quit() {
				t.Errorf("ctrl+c gave %v", msgs)
			}
		})
	}
}

// The mods finish loading under the open screen, and the screen says so.
func TestTheModsScreenWhileTheModsLoad(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod(), gitMod())
	r := newModRig(t, trueDark(), host, Options{})
	load := r.m.loadMods()
	r.talk()
	r.command("/mods")
	r.settle()
	r.wantOnScreen("loading…", "~/contactd/mods · 2 on, 0 off")
	r.kept = append(r.kept, load)
	r.settle()
	r.wantNotOnScreen("loading…")
	r.wantOnScreen("segment · /stats")
	// /status under it is not shown; a note for the conversation waits there.
	r.send(esc())
	r.wantBar("loaded", defaultLeft, defaultRight)
}

// The /mods screen fits the terminal it is drawn on, with the list, the
// details, the path input and a note.
func TestTheModsScreenFitsTheTerminal(t *testing.T) {
	t.Parallel()
	sizes := [][2]int{{100, 30}, {60, 16}, {140, 40}, {80, 24}, {79, 24}, {40, 12}, {30, 8}, {20, 5}, {10, 3}, {1, 1}}
	states := []struct {
		name string
		keys func(r *modRig)
	}{
		{name: "the list", keys: func(*modRig) {}},
		{name: "the last mod", keys: func(r *modRig) {
			r.send(special(tea.KeyDown))
			r.send(special(tea.KeyDown))
		}},
		{name: "the details", keys: func(r *modRig) { r.send(enter()) }},
		{name: "a path", keys: func(r *modRig) {
			r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
			r.send(tea.PasteMsg{Content: strings.Repeat("/a/long/way/down", 20)})
		}},
		{name: "a note", keys: func(r *modRig) {
			r.send(tea.KeyPressMsg{Code: 'i', Text: "i"})
			r.typeText("/nowhere/" + strings.Repeat("wide ", 30))
			r.send(enter())
			r.settle()
		}},
	}
	for _, caps := range []termcaps.Caps{trueDark(), light16(), unknown256(), noneASCII()} {
		for _, st := range states {
			for _, size := range sizes {
				host := newFakeMods(cacheMod(), gitMod(), standupMod())
				host.set("standup", mods.StatusFailed, "the module imports env.x, which a mod may not")
				r := newModRig(t, caps, host, Options{})
				r.load()
				r.talk()
				r.size(size[0], size[1])
				r.command("/mods")
				// A terminal too short for the list of commands still ran it.
				if r.m.screen != screenMods {
					t.Fatalf("%dx%d: /mods left the screen %v", size[0], size[1], r.m.screen)
				}
				r.settle()
				st.keys(r)
				what := fmt.Sprintf("%s, %s at %dx%d", st.name, caps.Glyphs, size[0], size[1])
				v := r.m.View()
				got := strings.Split(v.Content, "\n")
				if len(got) != size[1] {
					t.Errorf("%s: %d rows", what, len(got))
				}
				for i, row := range got {
					if w := ansi.StringWidth(row); w > size[0] {
						t.Errorf("%s: row %d is %d cells wide: %q", what, i, w, ansi.Strip(row))
					}
				}
				if c := v.Cursor; c != nil && (c.X < 0 || c.X >= size[0] || c.Y < 0 || c.Y >= size[1]) {
					t.Errorf("%s: the cursor is at %d,%d", what, c.X, c.Y)
				}
				if v.MouseMode != tea.MouseModeNone {
					t.Errorf("%s: the mouse is asked for", what)
				}
				if caps.Glyphs == termcaps.GlyphsASCII {
					if i := strings.IndexFunc(ansi.Strip(v.Content), func(c rune) bool { return c > 0x7f }); i >= 0 {
						t.Errorf("%s: a character outside ASCII at %d:\n%s", what, i, ansi.Strip(v.Content))
					}
				}
			}
		}
	}
}

// "/mods on", "off", "install" and "update" each do one thing to one mod
// without the screen, and say what became of it.
func TestModsCommandForms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line string
		// did is what the host was asked to do; "" is nothing.
		did string
		// fail is what the host answers with, when that is an error.
		fail error
		note string
		tone string
		// draft is what is left in the box.
		draft string
	}{
		{line: "/mods on git", did: "on git", note: "mod git is on", tone: toneDone},
		{line: "/mods ON git", did: "on git", note: "mod git is on", tone: toneDone},
		{line: "/mods off cache", did: "off cache", note: "mod cache is off", tone: toneDone},
		{line: "/mods update cache", did: "update cache", note: "mod cache is as it was installed: nothing changed", tone: toneDone},
		{line: "/mods update git", did: "update git", note: "mod git: updated: switch it on to use the new version"},
		{
			line: "/mods install dist/mods/standup", did: "install WORK/dist/mods/standup",
			note: "installed standup 0.2.0; it is off until /mods on standup, and /mods shows what it may read", tone: toneDone,
		},
		{
			line: `/mods install "~/my mods/standup"`, did: "install HOME/my mods/standup",
			note: "installed standup 0.2.0; it is off until /mods on standup, and /mods shows what it may read", tone: toneDone,
		},
		{
			line: "/mods install /nowhere", did: "install /nowhere",
			note: "install: open /nowhere/mod.toml: no such file or directory", draft: "/mods install /nowhere",
		},
		{
			line: "/mods on cache", did: "on cache", fail: errors.New("mods state: the disk is full"),
			note: "mods state: the disk is full", draft: "/mods on cache",
		},
		// What cannot be done is said at once, and the line stays to be put
		// right.
		{line: "/mods on", note: "usage: /mods [on|off|install|update <x>]", draft: "/mods on"},
		{line: "/mods on git cache", note: "usage: /mods [on|off|install|update <x>]", draft: "/mods on git cache"},
		{line: "/mods remove git", note: "usage: /mods [on|off|install|update <x>]", draft: "/mods remove git"},
		{line: `/mods install ""`, note: "usage: /mods [on|off|install|update <x>]", draft: `/mods install ""`},
		{line: "/mods on nosuch", note: `no mod is called "nosuch"; /mods lists them`, draft: "/mods on nosuch"},
		{line: "/mods off Cache", did: "off cache", note: "mod cache is off", tone: toneDone},
		{line: "/mods update No-Such", note: `no mod is called "no-such"; /mods lists them`, draft: "/mods update No-Such"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			host := newFakeMods(cacheMod(), gitMod())
			host.set("git", mods.StatusOff, "")
			host.changed["git"] = "0.2.0"
			work := t.TempDir()
			r := newModRig(t, trueDark(), host, Options{WorkDir: work})
			host.dist[filepath.Join(work, "dist", "mods", "standup")] = standupMod()
			host.dist[filepath.Join(r.m.opt.Home, "my mods", "standup")] = standupMod()
			if tc.fail != nil {
				host.fail[tc.did] = tc.fail
			}
			r.load()
			sess := r.talk()
			r.command(tc.line)
			if r.m.screen != screenChat {
				t.Fatalf("the screen is %v", r.m.screen)
			}
			if len(host.done()) != 0 {
				t.Fatalf("the line did %q before its command ran", host.done())
			}
			r.settle()
			var did []string
			if tc.did != "" {
				did = []string{strings.NewReplacer("WORK", work, "HOME", r.m.opt.Home).Replace(tc.did)}
			}
			if !slices.Equal(host.done(), did) {
				t.Errorf("did %q, want %q", host.done(), did)
			}
			if r.note() != tc.note || r.m.chat.noteTone != tc.tone || r.draft() != tc.draft {
				t.Errorf("says %q in the tone %q and leaves %q; want %q, %q, %q",
					r.note(), r.m.chat.noteTone, r.draft(), tc.note, tc.tone, tc.draft)
			}
			if len(sess.said) != 0 {
				t.Errorf("said %q", sess.said)
			}
		})
	}
}

// What "/mods on" and "/mods off" change is in the interface at once: the
// commands and the segments of the mod.
func TestSwitchingAModOnAndOff(t *testing.T) {
	t.Parallel()
	// Two mods with a command of the same name, and one that takes a core
	// command's name.
	other := mods.Manifest{Name: "other", Commands: []mods.CommandDecl{{Name: "stats"}, {Name: "help"}}}
	host := newFakeMods(cacheMod(), other)
	host.set("other", mods.StatusOff, "")
	host.says("cache", mods.OpSegment, "cache", wire.Reply{ABI: wire.ABI, Text: "cache avg 75%"})
	host.says("other", mods.OpCommand, "stats", wire.Reply{ABI: wire.ABI, Note: "other's stats"})
	host.says("other", mods.OpCommand, "help", wire.Reply{ABI: wire.ABI, Note: "other's help"})
	host.says("cache", mods.OpCommand, "stats", wire.Reply{ABI: wire.ABI, Note: "cache's stats"})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	r.talk()
	names := func() string {
		var out []string
		for _, spec := range r.m.cmds.Match("") {
			if spec.Source != "core" {
				out = append(out, spec.Name+" ("+spec.Source+")")
			}
		}
		return strings.Join(out, ", ")
	}
	if got := names(); got != "stats (mod cache)" {
		t.Fatalf("the mods' commands are %s", got)
	}
	r.command("/mods on other")
	r.settle()
	if got := names(); got != "other.help (mod other), other.stats (mod other), stats (mod cache)" {
		t.Fatalf("with both on the mods' commands are %s", got)
	}
	// Each name reaches its own mod, under the mod's own name for it.
	for line, note := range map[string]string{
		"/stats": "cache's stats", "/other.stats": "other's stats", "/other.help": "other's help",
	} {
		r.send(ctrl('c'))
		r.command(line)
		r.settle()
		if r.note() != note {
			t.Errorf("%s says %q, want %q", line, r.note(), note)
		}
	}
	// With the first one off its segment and its command are gone, and the
	// name is the other's: which mod has a name depends on which mods are
	// on, and not on the order they were switched on in.
	r.send(ctrl('c'))
	r.command("/mods off cache")
	r.settle()
	r.wantBar("cache off", defaultLeft, defaultRight)
	if got := names(); got != "other.help (mod other), stats (mod other)" {
		t.Errorf("with cache off the mods' commands are %s", got)
	}
	r.command("/stats")
	r.settle()
	if r.note() != "other's stats" {
		t.Errorf("with cache off /stats says %q", r.note())
	}
	if got := len(host.sent("cache", mods.OpSegment, "cache")); got != 1 {
		t.Errorf("the segment of a mod that is off was asked %d times in all", got)
	}
	r.command("/mods on cache")
	r.settle()
	r.wantBar("cache on again", defaultLeft, "cache avg 75% │ 12:00 ")
	if got := names(); got != "other.help (mod other), other.stats (mod other), stats (mod cache)" {
		t.Errorf("with cache on again the mods' commands are %s", got)
	}
	// A mod that cannot be loaded is not an error of the host's: the reason
	// is the note, and nothing of the mod is in the interface.
	r.command("/mods off other")
	r.settle()
	host.mu.Lock()
	host.broken["other"] = "the module imports env.x, which a mod may not"
	host.mu.Unlock()
	r.command("/mods on other")
	r.settle()
	if want := "mod other could not be switched on: the module imports env.x, which a mod may not"; r.note() != want || r.m.chat.noteTone != "" {
		t.Errorf("a mod that could not be loaded says %q, want %q", r.note(), want)
	}
	if got := names(); got != "stats (mod cache)" {
		t.Errorf("with a mod that could not be loaded the mods' commands are %s", got)
	}
}

// A client started without mods says so where the mods would be.
func TestModsWithoutAHost(t *testing.T) {
	t.Parallel()
	for why, want := range map[string]string{
		"": "mods are off: this client was started without them",
		"the client was started with -mods=false": "mods are off: the client was started with -mods=false",
	} {
		r := newModRig(t, trueDark(), nil, Options{ModsOff: why})
		if r.m.loadMods() != nil {
			t.Error("with no host there is something to load")
		}
		sess := r.talk()
		r.size(100, 44)
		for _, line := range []string{"/mods", "/mods on git"} {
			r.send(ctrl('c'))
			if cmd := r.command(line); cmd != nil || r.note() != want || r.draft() != line || r.m.screen != screenChat {
				t.Errorf("%s gives %v, says %q and leaves %q on the screen %v", line, cmd, r.note(), r.draft(), r.m.screen)
			}
		}
		r.send(ctrl('c'))
		r.command("/status")
		r.wantOnScreen("  mods         off: " + strings.TrimPrefix(want, "mods are off: "))
		// /reload has no mods to speak of.
		r.settle()
		r.command("/reload")
		r.settle()
		if strings.Contains(r.note(), "mods") || len(sess.said) != 0 {
			t.Errorf("/reload says %q", r.note())
		}
		// A mod's command in the registry has nothing behind it.
		mockMods(r.m.cmds)
		r.send(ctrl('c'))
		if cmd := r.command("/standup"); cmd != nil || r.note() != "/standup is a mod's command, and that mod is not running" {
			t.Errorf("/standup gives %v and says %q", cmd, r.note())
		}
	}
}

// /status has a line for each mod, and /reload has the host load the mods
// again and every one of them asked again.
func TestStatusAndReloadWithMods(t *testing.T) {
	t.Parallel()
	chart := mods.Manifest{Name: "chart", Version: "1.0", Read: []mods.Grant{mods.GrantMessages}, Renderers: []mods.RendererDecl{{Kind: "tool:python"}}}
	broken := mods.Manifest{Name: "broken"}
	host := newFakeMods(cacheMod(), gitMod(), standupMod(), chart, broken)
	host.set("standup", mods.StatusOff, "")
	host.set("broken", mods.StatusFailed, "mod.toml: line 3: read holds \"network\", which is no grant")
	drawings := 0
	host.answers("chart", mods.OpRender, "tool:python", func(mods.Input) (wire.Reply, error) {
		drawings++
		return wire.Reply{ABI: wire.ABI, Lines: []wire.Line{{{T: "drawing " + strconv.Itoa(drawings)}}}}, nil
	})
	host.answers("cache", mods.OpSegment, "cache", cacheAnswer)
	r := newModRig(t, trueDark(), host, Options{})
	r.size(100, 60)

	// Before the mods are loaded.
	r.talk()
	r.command("/status")
	r.wantOnScreen("  mod cache    loading, 0.3.1", "  mod standup  off, 0.2.0", "  mod broken   failed: mod.toml: line 3: read holds \"network\", which is no grant")
	r.load()
	r.command("/status")
	r.wantOnScreen(
		"  mod cache    on, 0.3.1: segment cache, /stats",
		"  mod chart    on, 1.0: draws tool:python",
		"  mod git      on, 0.1.0: segment branch",
	)
	r.wantOnScreen("drawing 1")

	// /reload: a mod that was switched on since is loaded, and the note
	// counts the mods that are on.
	host.set("standup", mods.StatusLoading, "")
	loads, asked := host.loads, len(host.sent("cache", mods.OpSegment, "cache"))
	r.command("/reload")
	if host.loads != loads {
		t.Fatal("/reload loaded the mods from the update loop")
	}
	r.settle()
	if host.loads != loads+1 {
		t.Errorf("/reload had the host load %d times", host.loads-loads)
	}
	if want := "read chat.toml again; glyph overrides: 0; read statusline.toml again; segments on: 10; mods on: 4"; r.note() != want {
		t.Errorf("/reload says\n%q, want\n%q", r.note(), want)
	}
	if _, ok := r.m.cmds.Lookup("standup"); !ok {
		t.Error("the command of the mod that was loaded is not registered")
	}
	// Every segment was asked again, and so was what a mod drew.
	if got := len(host.sent("cache", mods.OpSegment, "cache")); got != asked+1 {
		t.Errorf("/reload asked the segment %d times", got-asked)
	}
	r.wantOnScreen("drawing 2")
	r.wantNotOnScreen("drawing 1")
}

// Under a real program the mods are loaded once the interface is up, the
// segment of one is drawn, and the host is closed when the interface ends
// and not before.
func TestRunLoadsAndClosesTheModHost(t *testing.T) {
	t.Parallel()
	host := newFakeMods(cacheMod())
	host.says("cache", mods.OpSegment, "cache", wire.Reply{ABI: wire.ABI, Text: "cache avg 61%"})
	ada := client.Contact{ID: 1, Name: "ada", Provider: "deepseek", Model: "deepseek-flash"}
	in, keys := io.Pipe()
	t.Cleanup(func() { _ = keys.Close() }) // closing a pipe's writer never fails
	out := &tuitest.Recorder{}
	env, over := terminal(t, trueDark())
	done := make(chan error, 1)
	go func() {
		done <- Run(t.Context(), Options{
			Backend: &fakeBackend{contacts: []client.Contact{ada}}, Open: &ada, Mods: host,
			Now: fixedNow, Location: time.UTC, Env: env, Overrides: over,
			Program: []tea.ProgramOption{
				tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(100, 30),
				tea.WithEnvironment([]string{"TERM=xterm-256color"}),
			},
		})
	}()
	untilShown(t, out, 0, "cache avg 61%")
	host.mu.Lock()
	loads, closed := host.loads, host.closed
	host.mu.Unlock()
	if loads != 1 || closed != 0 {
		t.Errorf("while the interface runs the host was loaded %d times and closed %d times", loads, closed)
	}
	if _, err := keys.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed != 1 {
		t.Errorf("when Run returned the host had been closed %d times", host.closed)
	}
}

// modsMock brings the interface to the moment the Mods mock draws: the three
// example mods on, a renderer that is off, and the screen open on the first
// of them, which has answered a hundred calls. The build lists only what is
// installed, and has no panels.
func modsMock(t *testing.T, caps termcaps.Caps, width, height int) *modRig {
	t.Helper()
	mermaid := mods.Manifest{
		Name: "mermaid", Version: "0.1.2", Description: "Draws a diagram where a reply holds one.",
		Read: []mods.Grant{mods.GrantMessages}, Renderers: []mods.RendererDecl{{Kind: "code:mermaid"}},
	}
	host := newFakeMods(cacheMod(), gitMod(), standupMod(), mermaid)
	host.set("mermaid", mods.StatusOff, "")
	host.infos[0].Calls, host.infos[0].Median, host.infos[0].Slowest = 100, 400*time.Microsecond, 1900*time.Microsecond
	home := t.TempDir()
	r := newModRig(t, caps, host, Options{Dirs: conf.Dirs{Config: filepath.Join(home, ".config", "contactd")}, Home: home})
	r.load()
	r.talk()
	r.size(width, height)
	r.command("/mods")
	r.settle()
	return r
}

// The approved drawing of the /mods screen, at the size it was drawn for
// and at the smaller of the two sizes every screen must fit.
func TestModsMock(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{100, 30}, {60, 16}} {
		r := modsMock(t, trueDark(), size[0], size[1])
		rows := r.rows()
		if len(rows) != size[1] {
			t.Fatalf("at %dx%d the screen has %d rows:\n%s", size[0], size[1], len(rows), r.screen())
		}
		for i, row := range rows {
			if w := ansi.StringWidth(row); w != size[0] {
				t.Errorf("at %dx%d row %d is %d cells wide: %q", size[0], size[1], i, w, row)
			}
		}
		tuitest.Golden(t, fmt.Sprintf("mods-%dx%d.golden", size[0], size[1]), []byte(r.screen()+"\n"))
		if want := spread(size[0], "Mods", "~/.config/contactd/mods · 3 on, 1 off"); rows[0] != want {
			t.Errorf("at %dx%d the header is\n%q, want\n%q", size[0], size[1], rows[0], want)
		}
	}
	r := modsMock(t, trueDark(), 100, 30)
	rows := r.rows()
	for _, want := range []struct {
		row  int
		text string
	}{
		{1, strings.Repeat("─", 100)},
		{4, " ▸ [x] cache    0.3.1   segment · /stats          │  How much of each prompt the provider           "},
		{5, "   [x] git      0.1.0   segment                   │  served from its cache.                         "},
		{6, "   [ ] mermaid  0.1.2   renderer for code:mermaid │                                                 "},
		{7, "   [x] standup  0.2.0   /standup                  │  ADDS                                           "},
		{29, spread(100, "space on or off · enter details · i install from a path · u update", "esc back")},
	} {
		if rows[want.row] != want.text {
			t.Errorf("row %d is\n%q, want\n%q", want.row, rows[want.row], want.text)
		}
	}
	r.wantOnScreen("MAY READ", "model-call records of the open contact", "no messages, no network, no files",
		"sandboxed WebAssembly, in this process", "5 ms for each update", "LAST 100 UPDATES", "median 0.4 ms · slowest 1.9 ms")

	// The line about tmux is there when that is why images are half blocks,
	// and follows the terminal's answers while the screen is open.
	env := termcaps.Env{Term: "xterm-256color", Detected: termcaps.ColorTrue, Tmux: "/tmp/tmux-501/default,1,0"}
	tm := newModRig(t, trueDark(), newFakeMods(cacheMod()), Options{Env: env, Overrides: termcaps.Overrides{Theme: "dark"}})
	tm.load()
	tm.talk()
	tm.command("/mods")
	tm.settle()
	const line = "images are half blocks here: tmux allow-passthrough is off"
	// While the terminal behind tmux may still answer, nothing is said.
	tm.wantNotOnScreen(line)
	tm.send(probeDeadlineMsg{})
	tm.wantOnScreen(line)
	if got := tm.rows(); got[len(got)-1] != line+strings.Repeat(" ", 100-len(line)) || len(got) != 30 {
		t.Errorf("the last of %d rows is %q", len(got), got[len(got)-1])
	}
	// A screen opened afterwards says it from the start, and one on a
	// terminal that is not under tmux never does.
	tm.send(esc())
	tm.command("/mods")
	tm.wantOnScreen(line)
	r.send(probeDeadlineMsg{})
	r.wantNotOnScreen(line)
}

// A mod can answer anything. The real host makes it safe; the client does
// not count on that. Nothing of an answer reaches the terminal as more than
// text, and nothing of it makes a row wider than the terminal or the screen
// longer.
func TestAModsAnswerIsOnlyText(t *testing.T) {
	t.Parallel()
	const hostile = "\x1b]0;owned\x07zoë\x1b[2J\x1b[31m \u202ered\r\n\x00"
	wide := strings.Repeat("日本語👩‍👩‍👧", 40)
	loud := mods.Manifest{
		Name: "loud", Read: []mods.Grant{mods.GrantMessages},
		Segments:  []mods.SegmentDecl{{Name: "say", Refresh: mods.RefreshModelCall}},
		Commands:  []mods.CommandDecl{{Name: "shout", Usage: hostile, Summary: hostile + wide}},
		Renderers: []mods.RendererDecl{{Kind: "tool:python"}},
	}
	for name, text := range map[string]string{"hostile": hostile, "wide": wide} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lines := make([]wire.Line, 0, 300)
			for range 300 {
				lines = append(lines, wire.Line{{T: text, Tone: "no such tone"}, {T: text, Bold: true}})
			}
			host := newFakeMods(loud)
			host.says("loud", mods.OpSegment, "say", wire.Reply{ABI: wire.ABI, Text: text, Short: text, Tone: text})
			host.says("loud", mods.OpCommand, "shout", wire.Reply{ABI: wire.ABI, Lines: lines, Insert: text, Note: text, Tone: text})
			host.says("loud", mods.OpRender, "tool:python", wire.Reply{ABI: wire.ABI, Lines: lines})
			r := newModRig(t, trueDark(), host, Options{})
			r.load()
			r.talk()
			r.settle()
			check := func(what string) {
				t.Helper()
				for _, size := range [][2]int{{100, 30}, {60, 16}, {46, 12}, {30, 12}, {10, 5}} {
					r.size(size[0], size[1])
					r.settle()
					r.wantFits(what, size[0], size[1])
					if rows := r.rows(); len(rows) != size[1] {
						t.Errorf("%s at %v: the screen has %d rows", what, size, len(rows))
					}
					raw := r.m.View().Content
					for _, bad := range []string{"\x1b]0", "\x07", "\x1b[2J", "\x1b[31m", "\u202e", "\x00", "\r"} {
						if strings.Contains(raw, bad) {
							t.Errorf("%s at %v: the screen holds %q from a mod's answer", what, size, bad)
						}
					}
				}
				r.size(100, 30)
				r.settle()
			}
			check("a segment and a drawing")
			r.typeText("/sh")
			check("the list of commands")
			r.send(enter())
			r.settle()
			check("a command's answer")
			// A tone that is none is the plain one, and a block is at most as
			// long as the host lets an answer be.
			c := r.m.chat
			if c.note == "" || c.noteTone != "" {
				t.Errorf("the note is %q in the tone %q", c.note, c.noteTone)
			}
			if got := len(c.locals[len(c.locals)-1].lines); got != mods.MaxLines {
				t.Errorf("the block has %d lines", got)
			}
			// What a mod puts in the box is one line of it, and stays unsent.
			if draft := r.draft(); strings.ContainsAny(draft, "\x1b\x07\u202e\x00\r\n") || len([]rune(draft)) > mods.MaxSpanRunes {
				t.Errorf("the box holds %d characters of a mod's text: %q", len([]rune(draft)), draft)
			}
			r.send(ctrl('c'))
			r.command("/help shout")
			r.wantOnScreen("\nhelp\n", "  /shout ")
			check("the help")
			r.send(ctrl('c'))
			r.command("/status")
			check("the status")
			r.command("/mods")
			r.settle()
			check("the screen of the mods")
		})
	}
}

// With mods on, a segment in the bar and a mod that draws items, what a key
// press, a fragment of a reply and a mod's answer cost still does not grow
// with the conversation: no block is drawn for them, the list is not built
// again, and no mod is asked anything.
func TestModsDoNotMakeWorkGrowWithHistory(t *testing.T) {
	t.Parallel()
	chart := mods.Manifest{Name: "chart", Read: []mods.Grant{mods.GrantMessages}, Renderers: []mods.RendererDecl{{Kind: "tool:python"}}}
	host := newFakeMods(cacheMod(), chart)
	host.answers("cache", mods.OpSegment, "cache", cacheAnswer)
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	r.m.open(client.Contact{ID: 1, Name: "ada", Provider: "fake", Model: "fake-1"})
	sess := r.b.sessions[0]
	close(sess.updates)
	r.from(sess, live(), messages(t, 1, 10000, fixedNow()), attempt(t, 10001))
	r.settle()
	r.wantOnScreen("message 10000", "calls 1 │ 12:00 ")
	c := r.m.chat
	drawn, built, asked := c.win.Stats().Rendered, c.rebuilds, host.count()
	same := func(what string) {
		t.Helper()
		r.settle()
		r.screen()
		if got := c.win.Stats().Rendered; got != drawn || c.rebuilds != built || host.count() != asked {
			t.Errorf("%s drew %d blocks, rebuilt the list %d times and asked the mods %d times",
				what, got-drawn, c.rebuilds-built, host.count()-asked)
		}
	}
	r.typeText("x")
	same("a key press")
	r.send(ctrl('c'))
	r.from(sess, delta(core.Delta{Type: core.DeltaText, Text: "Counting "}))
	// The reply beginning is another state, and nothing follows that here.
	same("a streamed fragment")
	r.from(sess, delta(core.Delta{Type: core.DeltaText, Text: "them all."}))
	same("a second fragment")
	// A new model call asks the one segment that follows those, and its
	// answer draws the bar and nothing else.
	r.from(sess, attempt(t, 10002))
	r.settle()
	r.wantOnScreen("calls 2 │ 12:00 ")
	if got := c.win.Stats().Rendered; got != drawn || c.rebuilds != built || host.count() != asked+1 {
		t.Errorf("a model call drew %d blocks, rebuilt the list %d times and asked the mods %d times",
			got-drawn, c.rebuilds-built, host.count()-asked)
	}
}

// A mod that is told who the contact is hears of a new name.
func TestARenameTellsTheMods(t *testing.T) {
	t.Parallel()
	who := mods.Manifest{
		Name: "who", Read: []mods.Grant{mods.GrantContact},
		Segments: []mods.SegmentDecl{{Name: "name", Refresh: mods.RefreshTurn}},
	}
	host := newFakeMods(who)
	host.answers("who", mods.OpSegment, "name", func(in mods.Input) (wire.Reply, error) {
		return wire.Reply{ABI: wire.ABI, Text: "with " + in.Data.Contact.Name}, nil
	})
	r := newModRig(t, trueDark(), host, Options{})
	r.load()
	r.talk()
	r.wantBar("opened", defaultLeft, "with ada │ 12:00 ")
	r.command("/rename grace")
	r.settle()
	r.wantBar("renamed", defaultLeft, "with grace │ 12:00 ")
	if got := len(host.sent("who", mods.OpSegment, "name")); got != 2 {
		t.Errorf("the segment was asked %d times", got)
	}
}
