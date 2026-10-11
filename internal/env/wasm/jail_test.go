package wasm

import (
	"os"
	"path/filepath"
	"testing"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
)

func TestJail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "jail")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(dir, "sub"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(dir, "inside.txt"), filepath.Join(outside, "secret.txt")} {
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Links the guest could never make, as if something else had put them there.
	for name, target := range map[string]string{
		"file-link": filepath.Join(outside, "secret.txt"),
		"dir-link":  outside,
		"rel-link":  "../outside/secret.txt",
		"in-link":   "inside.txt",
		"doomed":    "../outside/secret.txt",
	} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	j := newJail(dir)

	t.Run("paths are pinned under the root", func(t *testing.T) {
		t.Parallel()
		for in, want := range map[string]string{
			"":                          ".",
			".":                         ".",
			"/":                         ".",
			"..":                        ".",
			"../..":                     ".",
			"inside.txt":                "inside.txt",
			"/inside.txt":               "inside.txt",
			"./sub/../inside.txt":       "inside.txt",
			"../outside/secret.txt":     "outside/secret.txt",
			"sub/../../../etc/passwd":   "etc/passwd",
			"/../../../../../etc/hosts": "etc/hosts",
			"sub/":                      "sub/",
			"sub/new/deeper.txt":        "sub/new/deeper.txt",
		} {
			if got, errno := j.confine(in, true); errno != 0 || got != want {
				t.Errorf("confine(%q) = %q, %v; want %q", in, got, errno, want)
			}
		}
	})

	t.Run("links are never followed", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"file-link", "dir-link", "rel-link", "in-link", "dir-link/secret.txt", "dir-link/new.txt", "dir-link/"} {
			if _, errno := j.OpenFile(name, experimentalsys.O_RDONLY, 0); errno != experimentalsys.EPERM {
				t.Errorf("OpenFile(%q) = %v, want EPERM", name, errno)
			}
			if _, errno := j.Stat(name); errno != experimentalsys.EPERM {
				t.Errorf("Stat(%q) = %v, want EPERM", name, errno)
			}
		}
		if _, errno := j.OpenFile("dir-link/new.txt", experimentalsys.O_CREAT|experimentalsys.O_WRONLY, 0o600); errno != experimentalsys.EPERM {
			t.Errorf("creating a file through a link = %v, want EPERM", errno)
		}
		if errno := j.Mkdir("dir-link/newdir", 0o700); errno != experimentalsys.EPERM {
			t.Errorf("Mkdir through a link = %v, want EPERM", errno)
		}
		if errno := j.Rename("inside.txt", "dir-link/moved.txt"); errno != experimentalsys.EPERM {
			t.Errorf("Rename through a link = %v, want EPERM", errno)
		}
		if errno := j.Link("file-link", "hard"); errno != experimentalsys.EPERM {
			t.Errorf("Link to a link = %v, want EPERM", errno)
		}
		if errno := j.Chmod("file-link", 0o777); errno != experimentalsys.EPERM {
			t.Errorf("Chmod through a link = %v, want EPERM", errno)
		}
		if errno := j.Utimens("file-link", 0, 0); errno != experimentalsys.EPERM {
			t.Errorf("Utimens through a link = %v, want EPERM", errno)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 1 {
			t.Errorf("something was written outside the jail: %v", entries)
		}
	})

	t.Run("links cannot be made but can be seen and removed", func(t *testing.T) {
		t.Parallel()
		for _, target := range []string{"/etc/passwd", "../outside", "inside.txt"} {
			if errno := j.Symlink(target, "made"); errno != experimentalsys.EPERM {
				t.Errorf("Symlink(%q) = %v, want EPERM", target, errno)
			}
		}
		if st, errno := j.Lstat("doomed"); errno != 0 || st.Mode&os.ModeSymlink == 0 {
			t.Errorf("Lstat of a link = %v, %v", st.Mode, errno)
		}
		if errno := j.Unlink("doomed"); errno != 0 {
			t.Errorf("Unlink of a link = %v", errno)
		}
		if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
			t.Errorf("unlinking the link touched its target: %v", err)
		}
	})

	t.Run("ordinary files work", func(t *testing.T) {
		t.Parallel()
		f, errno := j.OpenFile("sub/new.txt", experimentalsys.O_CREAT|experimentalsys.O_WRONLY, 0o600)
		if errno != 0 {
			t.Fatalf("create: %v", errno)
		}
		if _, errno := f.Write([]byte("hi")); errno != 0 {
			t.Errorf("write: %v", errno)
		}
		if errno := f.Close(); errno != 0 {
			t.Errorf("close: %v", errno)
		}
		if errno := j.Rename("sub/new.txt", "sub/renamed.txt"); errno != 0 {
			t.Errorf("rename: %v", errno)
		}
		if errno := j.Link("sub/renamed.txt", "sub/hard.txt"); errno != 0 {
			t.Errorf("hard link inside the jail: %v", errno)
		}
		if st, errno := j.Stat("/sub/../sub/hard.txt"); errno != 0 || st.Size != 2 {
			t.Errorf("stat: %+v, %v", st, errno)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "sub", "renamed.txt")); err != nil || string(b) != "hi" {
			t.Errorf("host view: %q, %v", b, err)
		}
	})
}
