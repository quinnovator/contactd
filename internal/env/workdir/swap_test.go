//go:build unix

package workdir

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// This test is inside the package, unlike the rest: it has to change the
// directory between the look at a contact's entry and the open of it, and
// only openSame stands between those two steps.

// A contact's entry is looked at and then opened. If it stops being the
// directory that was looked at in between, it must not be opened: the open
// follows a link, and a link would lead into another contact's directory.
func TestOpenSame(t *testing.T) {
	t.Parallel()
	const (
		own    = "contact 1's chart"
		secret = "contact 2's secret"
	)
	for _, tc := range []struct {
		name string
		// swap changes the tree after contact 1's entry was looked at.
		swap func(root string) error
		// want is what contact 1's file reads as. Empty means refused.
		want string
	}{
		{name: "left alone", swap: func(string) error { return nil }, want: own},
		{name: "swapped for a link to another contact", swap: func(root string) error {
			if err := os.Rename(filepath.Join(root, "1"), filepath.Join(root, "1.was")); err != nil {
				return err
			}
			return os.Symlink("2", filepath.Join(root, "1"))
		}},
		{name: "swapped for another contact's directory", swap: func(root string) error {
			if err := os.Rename(filepath.Join(root, "1"), filepath.Join(root, "1.was")); err != nil {
				return err
			}
			return os.Rename(filepath.Join(root, "2"), filepath.Join(root, "1"))
		}},
		{name: "removed", swap: func(root string) error {
			return os.Rename(filepath.Join(root, "1"), filepath.Join(root, "1.was"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for dir, data := range map[string]string{"1": own, "2": secret} {
				if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, dir, "f"), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })

			info, err := d.root.Lstat("1")
			if err != nil || !info.IsDir() {
				t.Fatalf("Lstat = %v, %v, want a directory", info, err)
			}
			if err := tc.swap(root); err != nil {
				t.Fatal(err)
			}
			sub, ok := d.openSame("1", info)
			if !ok {
				if tc.want != "" {
					t.Fatal("refused, want the directory")
				}
				return
			}
			defer func() { _ = sub.Close() }()
			f, err := sub.Open("f")
			if err != nil {
				t.Fatalf("opened a directory with no file in it: %v", err)
			}
			defer func() { _ = f.Close() }()
			data, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				t.Fatalf("opened a directory whose file holds %q, want a refusal", data)
			}
			if string(data) != tc.want {
				t.Errorf("the file holds %q, want %q", data, tc.want)
			}
		})
	}
}
