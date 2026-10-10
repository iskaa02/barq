package nvimpane

import (
	"os/exec"
	"syscall"
)

// procAttr puts nvim in its own process group (so its children can be killed
// with it) and makes the kernel kill it if barq dies.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// killProc kills nvim's whole process group.
func killProc(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
	}
}
