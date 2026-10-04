//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// errLocked means another barq already has this workspace open.
var errLocked = errors.New("workspace is locked")

// lockWorkspace takes an exclusive lock next to the workspace file so only
// one barq runs per directory. The OS drops the lock when the process exits,
// even on a crash, so it can never go stale. The returned file must stay
// open for as long as the lock is needed.
func lockWorkspace(ws *workspace) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(ws.path), 0o700); err != nil {
		return nil, err
	}
	path := strings.TrimSuffix(ws.path, ".json") + ".lock"
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, _ := os.ReadFile(path)
			if p := strings.TrimSpace(string(pid)); p != "" {
				return nil, fmt.Errorf("%w by pid %s", errLocked, p)
			}
			return nil, errLocked
		}
		return nil, err
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}
