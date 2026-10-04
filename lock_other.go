//go:build !unix

package main

import (
	"errors"
	"os"
)

var errLocked = errors.New("workspace is locked")

// lockWorkspace is a no-op where flock isn't available.
func lockWorkspace(*workspace) (*os.File, error) { return nil, nil }
