package conf

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile replaces a file's contents in one step. It writes a temporary
// file in the same directory and renames it over path, so that a reader, or
// a crash, finds the old file or the new one and never part of either.
//
// The file is readable and writable by its owner only (0600). A directory
// that is missing is made, for its owner only (0700). When the write fails
// the old file is as it was and nothing is left behind.
//
// When path is a symbolic link to a file, the file it points to is the one
// replaced, in its own directory, so that settings linked from elsewhere
// stay linked. A link to nothing is replaced by the file.
func WriteFile(path string, data []byte) error {
	if path == "" {
		return errors.New("write file: there is no path to write to")
	}
	dest := linked(path)
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	err = fill(tmp, data)
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// linked returns the file a symbolic link at path leads to, or path itself
// when it is not a link or leads nowhere.
func linked(path string) string {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return path
	}
	if dest, err := filepath.EvalSymlinks(path); err == nil {
		return dest
	}
	return path
}

// fill gives a temporary file its mode and contents, and closes it.
func fill(f *os.File, data []byte) error {
	// CreateTemp makes the file 0600 already. This says so, whatever a
	// later version of it does.
	err := f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	// The bytes must be on disk before the name points at them.
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
