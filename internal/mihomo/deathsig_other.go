//go:build !linux

package mihomo

import "os/exec"

// applyDeathSignal is a no-op where the kernel offers no parent-death signal.
func applyDeathSignal(cmd *exec.Cmd) {}
