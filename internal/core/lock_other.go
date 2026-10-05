//go:build !unix

package core

import (
	"errors"
	"os"
)

var ErrLocked = errors.New("workspace is locked")

// LockWorkspace is a no-op where flock isn't available.
func LockWorkspace(*Workspace) (*os.File, error) { return nil, nil }

// WithWriteLock just runs fn where flock isn't available.
func WithWriteLock(_ string, fn func() error) error { return fn() }
