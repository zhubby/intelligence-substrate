//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func Protect(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(command.Process.Pid)).Run()
	}
	command.WaitDelay = 5 * time.Second
}
