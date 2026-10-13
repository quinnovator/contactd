//go:build unix

package conf_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
)

// fileWithMode makes a file in a fresh directory and gives it exactly this
// mode, whatever the umask is.
func fileWithMode(t *testing.T, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "statusline.toml")
	if err := os.WriteFile(path, []byte("[[segment]]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSafeToExecuteFrom(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		perm   os.FileMode
		refuse string // part of the reason; "" means safe
	}{
		{"owner only", 0o600, ""},
		{"owner only, read only", 0o400, ""},
		{"others may read", 0o644, ""},
		{"others may read and run", 0o755, ""},
		{"no permissions at all", 0o000, ""},
		{"the group may write", 0o660, "written by its group (mode 0660)"},
		{"only the group may write", 0o620, "written by its group (mode 0620)"},
		{"everyone may write", 0o666, "written by its group (mode 0666)"},
		{"everyone but the group may write", 0o606, "written by everyone (mode 0606)"},
		{"only everyone may write", 0o602, "written by everyone (mode 0602)"},
		{"everything allowed", 0o777, "written by its group (mode 0777)"},
		{"the group may write and nobody may read", 0o020, "written by its group (mode 0020)"},
		{"everyone may write and nobody may read", 0o002, "written by everyone (mode 0002)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := fileWithMode(t, tc.perm)
			check := func(what, p string) {
				err := conf.SafeToExecuteFrom(p)
				switch {
				case tc.refuse == "" && err != nil:
					t.Errorf("%s with mode %04o was refused: %v", what, tc.perm, err)
				case tc.refuse != "" && err == nil:
					t.Errorf("%s with mode %04o was called safe", what, tc.perm)
				case tc.refuse != "" && !strings.Contains(err.Error(), tc.refuse):
					t.Errorf("%s: reason %q does not say %q", what, err, tc.refuse)
				case tc.refuse != "" && !strings.Contains(err.Error(), p):
					t.Errorf("%s: reason %q does not name the file", what, err)
				}
			}
			check("a file", path)
			// Through a link, it is the file's mode that counts.
			link := filepath.Join(t.TempDir(), "link.toml")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			check("a link to a file", link)
		})
	}
}

func TestSafeToExecuteFromOtherRefusals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.toml")
	if err := conf.SafeToExecuteFrom(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("a missing file: %v", err)
	}
	if err := conf.SafeToExecuteFrom(""); err == nil {
		t.Error("no path was called safe")
	}
	if err := conf.SafeToExecuteFrom(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a directory: %v", err)
	}
	dangling := filepath.Join(dir, "dangling.toml")
	if err := os.Symlink(missing, dangling); err != nil {
		t.Fatal(err)
	}
	if err := conf.SafeToExecuteFrom(dangling); err == nil {
		t.Error("a link to nothing was called safe")
	}
	toDir := filepath.Join(dir, "dir.toml")
	if err := os.Symlink(dir, toDir); err != nil {
		t.Fatal(err)
	}
	if err := conf.SafeToExecuteFrom(toDir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a link to a directory: %v", err)
	}
}

// Nothing a test can make without being root is owned by someone else, so
// this uses a file that is: the root directory's own. It is skipped for root.
func TestSafeToExecuteFromAnotherOwner(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root owns the files this test expects to belong to someone else")
	}
	for _, path := range []string{"/etc/hosts", "/etc/passwd", "/bin/sh"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			continue // not here, or refused for another reason: no use to this test
		}
		err = conf.SafeToExecuteFrom(path)
		if err == nil || !strings.Contains(err.Error(), "owned by another user") {
			t.Errorf("SafeToExecuteFrom(%s) = %v, want it refused as another user's", path, err)
		}
		// This user's own link to it does not make the file this user's.
		link := filepath.Join(t.TempDir(), "statusline.toml")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		err = conf.SafeToExecuteFrom(link)
		if err == nil || !strings.Contains(err.Error(), "owned by another user") {
			t.Errorf("SafeToExecuteFrom(a link to %s) = %v, want it refused as another user's", path, err)
		}
		return
	}
	t.Skip("no root-owned file to test with")
}
