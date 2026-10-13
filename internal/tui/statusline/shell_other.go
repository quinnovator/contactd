//go:build !unix

package statusline

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// NewShell returns the Runner that runs a command with /bin/sh -c. This
// system has no such shell, so the Runner runs nothing: every Run is an
// error that says shell segments are not supported.
func NewShell() Runner { return shell{} }

type shell struct{}

func (shell) Run(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("shell segments are not supported on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
