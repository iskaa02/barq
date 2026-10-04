//go:build !unix

package main

import (
	"errors"
	"os"
)

var errLocked = errors.New("workspace is locked")

// lockWorkspace is a no-op where flock isn't available.
func lockWorkspace(*workspace) (*os.File, error) { return nil, nil }

// withWriteLock just runs fn where flock isn't available.
func withWriteLock(_ string, fn func() error) error { return fn() }
