//go:build !unix

package conf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
)

func TestSafeToExecuteFromAlwaysRefuses(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "statusline.toml")
	if err := os.WriteFile(path, []byte("[[segment]]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := conf.SafeToExecuteFrom(path); err == nil {
		t.Error("a file was called safe on a system with no check")
	}
}
