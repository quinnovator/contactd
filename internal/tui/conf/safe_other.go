//go:build !unix

package conf

import (
	"fmt"
	"runtime"
)

// SafeToExecuteFrom says whether a file may be trusted with commands to
// run, as statusline.toml is. On Unix it is nil only when the file is owned
// by this user and neither its group nor everyone may write to it. This
// system has no such check, so it always refuses.
func SafeToExecuteFrom(path string) error {
	return fmt.Errorf("%s: who may write to a file cannot be checked on %s", path, runtime.GOOS)
}
