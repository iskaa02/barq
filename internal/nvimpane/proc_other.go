//go:build !linux

package nvimpane

import (
	"os/exec"
	"syscall"
)

func procAttr() *syscall.SysProcAttr { return nil }

func killProc(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
