//go:build unix

package workdir_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/env/workdir"
)

// What the files of the test tree hold. No refused path may ever read the
// second or the third.
const (
	own     = "contact 1's chart"
	secret  = "contact 2's secret"
	outside = "outside every contact"
)

// maxNameBytes is the longest path OpenFile accepts.
const maxNameBytes = 1024

// segment is one path element of the long names: five of them and their
// four slashes are exactly maxNameBytes.
func segment(c string) string { return strings.Repeat(c, 204) }

// atLimit is a path of exactly maxNameBytes bytes, and overLimit one
// byte longer. Both files exist.
func atLimit() string {
	return strings.Join([]string{segment("a"), segment("a"), segment("a"), segment("a"), segment("b")}, "/")
}

func overLimit() string {
	return strings.Join([]string{segment("a"), segment("a"), segment("a"), segment("a"), segment("c") + "c"}, "/")
}

// tree builds a directory of working directories and returns its path.
//
//	1/            a contact with files, links, a FIFO and a deep directory
//	2/            another contact, with a secret
//	3 -> 2        a contact directory that is a link
//	4             a contact "directory" that is a file
//	outside.txt   a file that belongs to no contact
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, data string) {
		t.Helper()
		must(os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0o600))
	}
	link := func(target, name string) {
		t.Helper()
		must(os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))))
	}
	must(os.MkdirAll(filepath.Join(root, "1", "sub"), 0o700))
	must(os.MkdirAll(filepath.Join(root, "2"), 0o700))
	write("1/latency.png", own)
	write("1/sub/a.png", own)
	write("1/empty", "")
	write("1/a b%2f..c.png", own)
	write("2/secret.txt", secret)
	write("4", "not a directory")
	write("outside.txt", outside)
	// A guest cannot make these, but a host process could.
	link("latency.png", "1/link-in")
	link("sub", "1/dirlink-in")
	link("../2/secret.txt", "1/link-out")
	link("../outside.txt", "1/link-up")
	link(filepath.Join(root, "2", "secret.txt"), "1/link-abs")
	link("../2", "1/dirlink-out")
	link("link-out", "1/link-chain")
	link("2", "3")
	must(syscall.Mkfifo(filepath.Join(root, "1", "fifo"), 0o600))

	// The deep paths are longer than some systems let one path be, so they
	// are made a step at a time, as Root does it.
	one, err := os.OpenRoot(filepath.Join(root, "1"))
	must(err)
	defer func() { _ = one.Close() }()
	must(one.MkdirAll(filepath.FromSlash(filepath.Dir(atLimit())), 0o700))
	must(one.WriteFile(filepath.FromSlash(atLimit()), []byte(own), 0o600))
	must(one.WriteFile(filepath.FromSlash(overLimit()), []byte(own), 0o600))
	return root
}

func open(t *testing.T, root string) *workdir.Dirs {
	t.Helper()
	d, err := workdir.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenFile(t *testing.T) {
	t.Parallel()
	if len(atLimit()) != maxNameBytes || len(overLimit()) != maxNameBytes+1 {
		t.Fatalf("the long names are %d and %d bytes, want %d and one more", len(atLimit()), len(overLimit()), maxNameBytes)
	}
	d := open(t, tree(t))

	for _, tc := range []struct {
		name    string
		contact core.ContactID
		path    string
		// want is the file's content. Empty with refused unset is an empty
		// file.
		want    string
		refused bool
	}{
		{name: "a file", contact: 1, path: "latency.png", want: own},
		{name: "a file in a directory", contact: 1, path: "sub/a.png", want: own},
		{name: "an empty file", contact: 1, path: "empty"},
		{name: "a name with a space, a percent and dots", contact: 1, path: "a b%2f..c.png", want: own},
		{name: "another contact's own file", contact: 2, path: "secret.txt", want: secret},
		{name: "a link that stays inside", contact: 1, path: "link-in", want: own},
		{name: "a directory link that stays inside", contact: 1, path: "dirlink-in/a.png", want: own},
		{name: "a path at the length limit", contact: 1, path: atLimit(), want: own},

		{name: "a path one byte over the limit", contact: 1, path: overLimit(), refused: true},
		{name: "a missing file", contact: 1, path: "missing.png", refused: true},
		{name: "a missing directory", contact: 1, path: "nowhere/a.png", refused: true},
		{name: "a file used as a directory", contact: 1, path: "latency.png/x", refused: true},
		{name: "a directory", contact: 1, path: "sub", refused: true},
		{name: "a directory with a trailing slash", contact: 1, path: "sub/", refused: true},
		{name: "the working directory itself", contact: 1, path: ".", refused: true},
		{name: "no path", contact: 1, path: "", refused: true},
		{name: "the parent", contact: 1, path: "..", refused: true},
		{name: "a climb to another contact", contact: 1, path: "../2/secret.txt", refused: true},
		{name: "a climb out of every contact", contact: 1, path: "../outside.txt", refused: true},
		{name: "a climb from inside a directory", contact: 1, path: "sub/../../2/secret.txt", refused: true},
		{name: "a climb that would come back", contact: 1, path: "sub/../latency.png", refused: true},
		{name: "a dot element", contact: 1, path: "./latency.png", refused: true},
		{name: "an absolute path", contact: 1, path: "/etc/hosts", refused: true},
		{name: "a doubled slash", contact: 1, path: "sub//a.png", refused: true},
		{name: "a NUL", contact: 1, path: "latency.png\x00.txt", refused: true},
		{name: "a link to another contact", contact: 1, path: "link-out", refused: true},
		{name: "a link out of every contact", contact: 1, path: "link-up", refused: true},
		{name: "an absolute link", contact: 1, path: "link-abs", refused: true},
		{name: "a link to a link that leads out", contact: 1, path: "link-chain", refused: true},
		{name: "a directory link to another contact", contact: 1, path: "dirlink-out/secret.txt", refused: true},
		{name: "a FIFO nobody writes to", contact: 1, path: "fifo", refused: true},
		{name: "a contact directory that is a link", contact: 3, path: "secret.txt", refused: true},
		{name: "a contact directory that is a file", contact: 4, path: "x", refused: true},
		{name: "a contact with no directory", contact: 9, path: "latency.png", refused: true},
		{name: "contact zero", contact: 0, path: "latency.png", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := d.OpenFile(tc.contact, tc.path)
			if tc.refused {
				if f != nil {
					data, _ := io.ReadAll(f)
					_ = f.Close()
					t.Fatalf("opened a file holding %q, want a refusal", data)
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("err = %v, want fs.ErrNotExist like every other refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the file", err)
			}
			defer func() { _ = f.Close() }()
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("Stat = %v, %v, want a regular file", info, err)
			}
			data, err := io.ReadAll(f)
			if err != nil || string(data) != tc.want {
				t.Errorf("read %q, %v, want %q", data, err, tc.want)
			}
		})
	}
}

// A refusal must not say why: the error for a link that leads out reads the
// same as the one for a file that was never there.
func TestRefusalsLookAlike(t *testing.T) {
	t.Parallel()
	d := open(t, tree(t))
	_, missing := d.OpenFile(1, "missing")
	if missing == nil {
		t.Fatal("a missing file opened")
	}
	for _, path := range []string{"link-out", "sub", "fifo"} {
		_, err := d.OpenFile(1, path)
		if err == nil {
			t.Fatalf("%s opened", path)
		}
		want := strings.ReplaceAll(missing.Error(), "missing", path)
		if err.Error() != want {
			t.Errorf("%s: %q, want %q", path, err, want)
		}
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	root := tree(t)
	for _, tc := range []struct {
		name, path string
	}{
		{"a directory that is not there", filepath.Join(root, "nowhere")},
		{"a file", filepath.Join(root, "outside.txt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := workdir.Open(tc.path)
			if err == nil {
				_ = d.Close()
				t.Fatal("opened, want an error")
			}
			if !strings.HasPrefix(err.Error(), "workdir: ") {
				t.Errorf("err = %q, want it to say where it came from", err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	d, err := workdir.Open(tree(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.OpenFile(1, "latency.png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// A file opened before the close is the caller's, and still reads.
	if data, err := io.ReadAll(f); err != nil || string(data) != own {
		t.Errorf("read after close = %q, %v, want %q", data, err, own)
	}
	if again, err := d.OpenFile(1, "latency.png"); !errors.Is(err, fs.ErrNotExist) {
		if again != nil {
			_ = again.Close()
		}
		t.Errorf("open after close: %v, want fs.ErrNotExist", err)
	}
}
