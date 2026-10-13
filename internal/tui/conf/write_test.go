//go:build unix

package conf_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
)

// names lists a directory, so a test can see that nothing was left in it.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestWriteFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// before is the file already there, with its mode; nil is none.
		before     []byte
		beforeMode os.FileMode
		data       []byte
	}{
		{"a new file", nil, 0, []byte("color = \"16\"\n")},
		{"an empty file", nil, 0, nil},
		{"over a file of the same mode", []byte("old"), 0o600, []byte("new")},
		{"over a file others could read", []byte("old and longer than the new one"), 0o644, []byte("new")},
		{"over a file others could write", []byte("old"), 0o666, []byte("new, and longer than the old one")},
		{"over a file nobody may write", []byte("old"), 0o400, []byte("new")},
		{"a large file", nil, 0, bytes.Repeat([]byte("0123456789abcdef"), 1<<16)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "statusline.toml")
			if tc.before != nil {
				if err := os.WriteFile(path, tc.before, 0o600); err != nil {
					t.Fatal(err)
				}
				// Chmod, since the mode WriteFile asks for passes through the umask.
				if err := os.Chmod(path, tc.beforeMode); err != nil {
					t.Fatal(err)
				}
			}
			if err := conf.WriteFile(path, tc.data); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Errorf("the file holds %d bytes, want the %d written", len(got), len(tc.data))
			}
			if m := mode(t, path); m != 0o600 {
				t.Errorf("the file's mode is %04o, want 0600", m)
			}
			if got := names(t, dir); len(got) != 1 || got[0] != "statusline.toml" {
				t.Errorf("the directory holds %q, want the file alone", got)
			}
		})
	}
}

func TestWriteFileMakesTheDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	path := filepath.Join(base, "config", "contactd", "mods.toml")
	if err := conf.WriteFile(path, []byte("[mod.cache]\non = true\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, dir := range []string{filepath.Join(base, "config"), filepath.Join(base, "config", "contactd")} {
		if m := mode(t, dir); m != 0o700 {
			t.Errorf("%s has mode %04o, want 0700", dir, m)
		}
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("the file's mode is %04o, want 0600", m)
	}

	// A directory that is already there keeps its mode.
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := conf.WriteFile(filepath.Join(shared, "chat.toml"), nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if m := mode(t, shared); m != 0o755 {
		t.Errorf("an existing directory's mode became %04o", m)
	}
}

// The new file takes the name; the old one is never written to. A reader
// that opened the file before the write still has the old contents, whole.
func TestWriteFileReplacesByRename(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "chat.toml")
	oldData := bytes.Repeat([]byte("old "), 4096)
	if err := conf.WriteFile(path, oldData); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close() //nolint:errcheck // read only
	before, err := reader.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if err := conf.WriteFile(path, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, oldData) {
		t.Errorf("the file open before the write now reads %d bytes, want the old %d: it was written in place", len(got), len(oldData))
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the path names the same file as before: it was written in place")
	}
}

// Settings are often linked from a directory kept elsewhere. A write through
// the link replaces the file it leads to, and the link stays a link.
func TestWriteFileThroughALink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// link makes the link at path, to the file at real.
		link func(t *testing.T, path, real string)
	}{
		{"a link to a file in another directory", func(t *testing.T, path, real string) {
			t.Helper()
			if err := os.Symlink(real, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a relative link", func(t *testing.T, path, real string) {
			t.Helper()
			rel, err := filepath.Rel(filepath.Dir(path), real)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rel, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to a link", func(t *testing.T, path, real string) {
			t.Helper()
			between := filepath.Join(filepath.Dir(path), "between.toml")
			if err := os.Symlink(real, between); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("between.toml", path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			config, dotfiles := filepath.Join(base, "config"), filepath.Join(base, "dotfiles")
			for _, dir := range []string{config, dotfiles} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			path, real := filepath.Join(config, "statusline.toml"), filepath.Join(dotfiles, "sl.toml")
			if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(real, 0o644); err != nil {
				t.Fatal(err)
			}
			tc.link(t, path, real)
			before := names(t, config)

			if err := conf.WriteFile(path, []byte("new")); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link is no longer a link: %v, %v", info, err)
			}
			if info, err := os.Lstat(real); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Errorf("the linked file is not a regular file of mode 0600: %v, %v", info, err)
			}
			for _, p := range []string{path, real} {
				if got, err := os.ReadFile(p); err != nil || string(got) != "new" {
					t.Errorf("%s reads %q, %v; want the new contents", p, got, err)
				}
			}
			if got := names(t, config); !slices.Equal(got, before) {
				t.Errorf("the link's directory holds %q, want %q as before", got, before)
			}
			if got := names(t, dotfiles); len(got) != 1 || got[0] != "sl.toml" {
				t.Errorf("the file's directory holds %q, want the file alone", got)
			}
		})
	}
}

// A link that leads nowhere has no file to replace, so the file takes the
// link's place.
func TestWriteFileOverADanglingLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mods.toml")
	if err := os.Symlink(filepath.Join(dir, "gone", "mods.toml"), path); err != nil {
		t.Fatal(err)
	}
	if err := conf.WriteFile(path, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("the path is not a regular file of mode 0600: %v, %v", info, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Errorf("the file reads %q, %v", got, err)
	}
	if got := names(t, dir); len(got) != 1 || got[0] != "mods.toml" {
		t.Errorf("the directory holds %q, want the file alone", got)
	}
}

// While one writer replaces the file again and again, every read finds one
// whole version or the other: never an empty file, a part, or no file.
func TestWriteFileIsAtomic(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "statusline.toml")
	versions := [2][]byte{
		bytes.Repeat([]byte("a"), 200<<10),
		bytes.Repeat([]byte("b"), 300<<10),
	}
	if err := conf.WriteFile(path, versions[0]); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("a read during a write failed: %v", err)
					return
				}
				if !bytes.Equal(got, versions[0]) && !bytes.Equal(got, versions[1]) {
					t.Errorf("a read during a write saw %d bytes, which is neither version", len(got))
					return
				}
			}
		})
	}
	for i := range 200 {
		if err := conf.WriteFile(path, versions[(i+1)%2]); err != nil {
			t.Errorf("WriteFile: %v", err)
			break
		}
	}
	close(stop)
	readers.Wait()
}

func TestWriteFileFails(t *testing.T) {
	t.Parallel()

	t.Run("no path", func(t *testing.T) {
		t.Parallel()
		if err := conf.WriteFile("", []byte("x")); err == nil {
			t.Error("WriteFile with no path succeeded")
		}
	})

	// The rename is the last step, so this fails after the temporary file
	// has been written: it must not be left behind.
	t.Run("the path is a directory with something in it", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "chat.toml")
		if err := os.MkdirAll(filepath.Join(path, "inside"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := conf.WriteFile(path, []byte("x")); err == nil {
			t.Error("WriteFile over a directory succeeded")
		}
		if got := names(t, dir); len(got) != 1 || got[0] != "chat.toml" {
			t.Errorf("the directory holds %q after a failed write, want only what was there", got)
		}
		if got := names(t, path); len(got) != 1 || got[0] != "inside" {
			t.Errorf("the directory in the way holds %q", got)
		}
	})

	t.Run("the directory is a file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		blocker := filepath.Join(dir, "contactd")
		if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := conf.WriteFile(filepath.Join(blocker, "chat.toml"), []byte("x")); err == nil {
			t.Error("WriteFile under a file succeeded")
		}
		if got, err := os.ReadFile(blocker); err != nil || string(got) != "in the way" {
			t.Errorf("the file in the way is now %q, %v", got, err)
		}
	})

	t.Run("the directory may not be written to", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root may write anywhere")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "chat.toml")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // so the directory can be removed
		err := conf.WriteFile(path, []byte("new"))
		if err == nil {
			t.Error("WriteFile into a read-only directory succeeded")
		}
		if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old" {
			t.Errorf("after a failed write the file is %q, %v; want the old contents", got, readErr)
		}
		if got := names(t, dir); len(got) != 1 {
			t.Errorf("the directory holds %q after a failed write", got)
		}
	})
}
