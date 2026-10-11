package wasm

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/experimental/sysfs"
	"github.com/tetratelabs/wazero/sys"
)

// jail is a contact's working directory as the guest sees it.
//
// wazero's directory mount resolves paths with the host's own open(2), so by
// itself it follows symbolic links wherever they point: a guest could link to
// /etc or to a sibling contact and read straight through. The jail closes
// that in three layers:
//
//   - every path is cleaned as if rooted at the mount, so ".." cannot climb;
//   - the guest cannot create a symbolic link at all;
//   - no operation walks through a link that got there some other way.
//
// The last check is not atomic with the operation that follows it. That is
// acceptable only because of the second rule: nothing inside the sandbox can
// plant a link between the check and the use.
type jail struct {
	inner experimentalsys.FS
	dir   string
}

func newJail(dir string) *jail {
	return &jail{inner: sysfs.DirFS(dir), dir: dir}
}

// confine maps a guest path to one that provably stays inside the jail.
// through says the operation would follow a link in the final component,
// rather than act on the link itself.
func (j *jail) confine(name string, through bool) (string, experimentalsys.Errno) {
	// wazero marks "must be a directory" with a trailing slash.
	slash := strings.HasSuffix(name, "/")
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	if clean == "" {
		return ".", 0
	}
	host := j.dir
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		host = filepath.Join(host, part)
		info, err := os.Lstat(host)
		if err != nil {
			break // missing, so nothing below it exists either
		}
		if info.Mode()&fs.ModeSymlink != 0 && (through || slash || i < len(parts)-1) {
			return "", experimentalsys.EPERM
		}
	}
	if slash {
		clean += "/"
	}
	return clean, 0
}

func (j *jail) OpenFile(name string, flag experimentalsys.Oflag, perm fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	name, errno := j.confine(name, flag&experimentalsys.O_NOFOLLOW == 0)
	if errno != 0 {
		return nil, errno
	}
	return j.inner.OpenFile(name, flag, perm)
}

func (j *jail) Lstat(name string) (sys.Stat_t, experimentalsys.Errno) {
	name, errno := j.confine(name, false)
	if errno != 0 {
		return sys.Stat_t{}, errno
	}
	return j.inner.Lstat(name)
}

func (j *jail) Stat(name string) (sys.Stat_t, experimentalsys.Errno) {
	name, errno := j.confine(name, true)
	if errno != 0 {
		return sys.Stat_t{}, errno
	}
	return j.inner.Stat(name)
}

func (j *jail) Mkdir(name string, perm fs.FileMode) experimentalsys.Errno {
	name, errno := j.confine(name, true)
	if errno != 0 {
		return errno
	}
	return j.inner.Mkdir(name, perm)
}

func (j *jail) Chmod(name string, perm fs.FileMode) experimentalsys.Errno {
	name, errno := j.confine(name, true)
	if errno != 0 {
		return errno
	}
	return j.inner.Chmod(name, perm)
}

func (j *jail) Rename(from, to string) experimentalsys.Errno {
	from, errno := j.confine(from, false)
	if errno != 0 {
		return errno
	}
	to, errno = j.confine(to, false)
	if errno != 0 {
		return errno
	}
	return j.inner.Rename(from, to)
}

func (j *jail) Rmdir(name string) experimentalsys.Errno {
	name, errno := j.confine(name, true)
	if errno != 0 {
		return errno
	}
	return j.inner.Rmdir(name)
}

func (j *jail) Unlink(name string) experimentalsys.Errno {
	name, errno := j.confine(name, false)
	if errno != 0 {
		return errno
	}
	return j.inner.Unlink(name)
}

// Link is allowed: a hard link can only name a file already inside the jail.
func (j *jail) Link(oldName, newName string) experimentalsys.Errno {
	oldName, errno := j.confine(oldName, true)
	if errno != 0 {
		return errno
	}
	newName, errno = j.confine(newName, true)
	if errno != 0 {
		return errno
	}
	return j.inner.Link(oldName, newName)
}

// Symlink always fails. See the type comment.
func (*jail) Symlink(string, string) experimentalsys.Errno {
	return experimentalsys.EPERM
}

func (j *jail) Readlink(name string) (string, experimentalsys.Errno) {
	name, errno := j.confine(name, false)
	if errno != 0 {
		return "", errno
	}
	return j.inner.Readlink(name)
}

func (j *jail) Utimens(name string, atim, mtim int64) experimentalsys.Errno {
	name, errno := j.confine(name, true)
	if errno != 0 {
		return errno
	}
	return j.inner.Utimens(name, atim, mtim)
}

// fsConfig is the guest's whole filesystem: the working directory at "/",
// plus the base's read-only mounts.
func (img *image) fsConfig(dir string) (wazero.FSConfig, error) {
	custom, ok := wazero.NewFSConfig().(sysfs.FSConfig)
	if !ok {
		return nil, errors.New("wazero FSConfig does not take a custom filesystem")
	}
	cfg := custom.WithSysFSMount(newJail(dir), "/")
	for _, m := range img.base.Mounts {
		cfg = cfg.WithReadOnlyDirMount(m.HostDir, m.GuestPath)
	}
	return cfg, nil
}
