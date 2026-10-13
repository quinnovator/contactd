package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/mods"
)

// buildExample builds one of the shipped example mods the way the mods:build
// task does, into a directory that also holds its manifest, and returns that
// directory: what "/mods install" is given.
func buildExample(t *testing.T, name string) string {
	t.Helper()
	dist := filepath.Join(t.TempDir(), "dist", "mods", name)
	if err := os.MkdirAll(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "build",
		"-buildmode=c-shared", "-trimpath", "-ldflags=-s -w",
		"-o", filepath.Join(dist, name+".wasm"), "github.com/quinnovator/contactd/examples/mods/"+name)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, msg)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "..", "examples", "mods", name, mods.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, mods.ManifestFile), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return dist
}

// The real host and the shipped example mods, built here, under the model:
// each is installed and switched on with the commands a person would type,
// and then a segment of two of them is in the bar and a command of two of
// them does what it says.
func TestTheExampleModsUnderTheModel(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dirs := conf.Dirs{Config: filepath.Join(home, ".config", "contactd"), Cache: filepath.Join(home, ".cache", "contactd")}
	// The host's clock stands still, so no call is ever over its budget
	// however slow the machine is under the race detector; a mod is given a
	// minute before it is stopped.
	host, err := mods.Open(mods.Options{
		Dir: dirs.ModsDir(), StatePath: dirs.ModsState(), CacheDir: dirs.WasmCache(),
		Kill: time.Minute, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	r := newModRig(t, trueDark(), nil, Options{Mods: host, Dirs: dirs, Home: home, WorkDir: repo(t, "main")})
	r.load()
	sess := r.talk()
	r.wantBar("with nothing installed", defaultLeft, defaultRight)

	// Installed, a mod is off, and nothing of it is in the interface.
	r.command("/mods install " + buildExample(t, "cache"))
	r.settle()
	if want := "installed cache 0.3.1; it is off until /mods on cache, and /mods shows what it may read"; r.note() != want {
		t.Fatalf("the install says %q", r.note())
	}
	if _, ok := r.m.cmds.Lookup("stats"); ok {
		t.Error("the command of a mod that is off is registered")
	}
	r.wantBar("installed, off", defaultLeft, defaultRight)

	// Switched on, its segment says what the one model call of the history
	// comes to, and its command is there.
	r.command("/mods on cache")
	r.settle()
	if r.note() != "mod cache is on" {
		t.Fatalf("switching the mod on says %q", r.note())
	}
	r.wantBar("cache on", defaultLeft, "cache avg 75% │ 12:00 ")
	r.command("/stats")
	r.settle()
	r.wantOnScreen("\ncache\n", "  Mon 12 Oct  1 call  2.0k in  cache 75%")
	// Another model call, with less of it from the cache: the average of the
	// two, in the warn tone's short form at a width that has no room.
	r.from(sess, attempt(t, 6))
	r.settle()
	r.wantBar("two calls", " ● live │ fake-1 │ cache 50% │ 1.0k tok │ 0.9s", "cache avg 67% │ 12:00 ")
	r.command("/stats nine")
	r.settle()
	if r.note() != "usage: /stats [days], with days from 1 to 366" || r.draft() != "/stats nine" {
		t.Errorf("/stats nine says %q and leaves %q", r.note(), r.draft())
	}
	r.send(ctrl('c'))

	// The branch, which the client reads for the mod.
	r.command("/mods install " + buildExample(t, "git"))
	r.settle()
	r.command("/mods on git")
	r.settle()
	r.wantBar("git on", " ● live │ fake-1 │ cache 50% │ 1.0k tok │ 0.9s", "cache avg 67% │ ⎇ main │ 12:00 ")

	// A command that only puts text in the box.
	r.command("/mods install " + buildExample(t, "standup"))
	r.settle()
	r.command("/mods on standup")
	r.settle()
	r.command("/standup")
	r.settle()
	if want := "Summarize what we did yesterday (Sun 11 Oct) for the team: done, next, blocked."; r.draft() != want || len(sess.said) != 0 {
		t.Errorf("/standup left %q and said %q", r.draft(), sess.said)
	}
	r.send(ctrl('c'))

	// The screen lists the three as the host has them, with the calls made.
	r.command("/mods")
	r.settle()
	r.wantOnScreen("~/.config/contactd/mods · 3 on, 0 off", "[x] cache", "[x] git", "[x] standup", "LAST 100 UPDATES", "median ")
	r.send(esc())

	// What was switched on is on at the next start, from the state file the
	// host wrote: a second host over the same directories loads all three.
	again, err := mods.Open(mods.Options{Dir: dirs.ModsDir(), StatePath: dirs.ModsState(), CacheDir: dirs.WasmCache(), Kill: time.Minute, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := again.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	loaded := again.Load(t.Context())
	on := make([]string, 0, len(loaded))
	for _, info := range loaded {
		on = append(on, info.Manifest.Name+" "+info.Status.String())
	}
	if got := strings.Join(on, ", "); got != "cache on, git on, standup on" {
		t.Errorf("at the next start the mods are %s", got)
	}
}
