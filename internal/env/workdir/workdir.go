// Package workdir reads the tier-0 working directories from the host side.
//
// The guest's view of a directory is internal/env/wasm's business. This is
// the other view: a server process that wants one file out of it, named by a
// path it was handed over the network.
//
// A guest chooses the names in its directory and the bytes in its files, and
// the path comes from a client. So nothing here trusts either: a path may
// not climb out, a link may not lead out, and only a regular file is opened.
package workdir

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/quinnovator/contactd/internal/core"
)

// maxNameBytes is the longest path OpenFile accepts. A longer one is refused
// before the kernel is asked about it.
const maxNameBytes = 1024

// Dirs is the directory that holds every contact's working directory. Each
// contact's is the entry named by its ID. It is safe for concurrent use.
type Dirs struct {
	root *os.Root
}

// Open opens fsRoot, which must exist and be a directory.
func Open(fsRoot string) (*Dirs, error) {
	root, err := os.OpenRoot(fsRoot)
	if err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}
	return &Dirs{root: root}, nil
}

// Close releases the directory. Files already open stay readable; every
// later OpenFile is refused.
func (d *Dirs) Close() error {
	if err := d.root.Close(); err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	return nil
}

// OpenFile opens a regular file in a contact's working directory for
// reading. name is slash-separated and relative to that directory.
//
// Anything else, whatever the reason, is fs.ErrNotExist: a path that climbs
// out, a link that leads out, a directory, a FIFO, a name that is too long,
// a contact with no directory. A caller cannot tell these apart, so neither
// can whoever sent the path. A link that stays inside the contact's
// directory is followed.
func (d *Dirs) OpenFile(id core.ContactID, name string) (fs.File, error) {
	if len(name) > maxNameBytes || name == "." || !fs.ValidPath(name) || strings.ContainsRune(name, 0) {
		return nil, notExist(name)
	}
	dir := id.String()
	// Lstat, not Stat: Root follows a link that stays inside it, and a link
	// here would hand one contact another contact's directory.
	info, err := d.root.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, notExist(name)
	}
	sub, ok := d.openSame(dir, info)
	if !ok {
		return nil, notExist(name)
	}
	defer func() { _ = sub.Close() }() // a read-only handle; nothing to lose
	// O_NONBLOCK: opening a FIFO that has no writer would otherwise never
	// return. It changes nothing about reading a regular file.
	f, err := sub.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, notExist(name)
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close() // nothing was read
		return nil, notExist(name)
	}
	return f, nil
}

// openSame opens the entry dir and requires it to be the directory that
// info, from an Lstat of it, describes.
//
// The look and the open are two steps, and the open follows a link. So an
// entry that was a directory when it was looked at, and was swapped for a
// link before it was opened, would open whatever the link names. Comparing
// the two closes that gap.
func (d *Dirs) openSame(dir string, info fs.FileInfo) (*os.Root, bool) {
	sub, err := d.root.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	if opened, err := sub.Stat("."); err != nil || !os.SameFile(info, opened) {
		_ = sub.Close() // a read-only handle; nothing to lose
		return nil, false
	}
	return sub, true
}

// notExist is the one answer to every refused path.
func notExist(name string) error {
	return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
