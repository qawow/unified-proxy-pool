//go:build linux

package mihomo

import (
	"os/exec"
	"syscall"
)

// applyDeathSignal asks the kernel to SIGTERM the child when this process dies,
// however it dies (log.Fatal, panic, SIGKILL, or exec into a new binary).
func applyDeathSignal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
