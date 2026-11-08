//go:build !windows

package process

import (
	"os/exec"
	"syscall"
	"time"
)

// Protect gives each command a process group so cancellation also reaches the
// children spawned by compilers, shells, CLI tunnels and publishing programs.
func Protect(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 3 * time.Second
}
