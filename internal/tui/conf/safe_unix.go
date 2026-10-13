//go:build unix

package conf

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// SafeToExecuteFrom says whether a file may be trusted with commands to
// run, as statusline.toml is. It returns nil only when the file is a regular
// file owned by this user and neither its group nor everyone may write to
// it: otherwise someone else could have put the commands there. The error
// says which of those failed.
//
// A symbolic link is followed, since settings are often linked from
// elsewhere, but the link must be this user's too.
//
// Off Unix it always refuses.
func SafeToExecuteFrom(path string) error {
	uid := os.Geteuid()
	link, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("not safe to run commands from: %w", err)
	}
	if link.Mode()&fs.ModeSymlink == 0 {
		return checkFile(path, link, uid)
	}
	if owner, ok := ownerOf(link); !ok || owner != uid {
		return fmt.Errorf("%s is a link owned by another user", path)
	}
	target, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("not safe to run commands from: %w", err)
	}
	return checkFile(path, target, uid)
}

// checkFile is the rule itself, on what stat said about the file.
func checkFile(path string, info fs.FileInfo, uid int) error {
	owner, ok := ownerOf(info)
	switch {
	case !ok:
		return fmt.Errorf("%s: cannot tell who owns it", path)
	case owner != uid:
		return fmt.Errorf("%s is owned by another user (uid %d)", path, owner)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case info.Mode().Perm()&0o020 != 0:
		return fmt.Errorf("%s can be written by its group (mode %04o); chmod go-w fixes that", path, info.Mode().Perm())
	case info.Mode().Perm()&0o002 != 0:
		return fmt.Errorf("%s can be written by everyone (mode %04o); chmod go-w fixes that", path, info.Mode().Perm())
	}
	return nil
}

func ownerOf(info fs.FileInfo) (uid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
