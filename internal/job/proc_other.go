//go:build !windows

package job

import "os/exec"

// setNoWindow is a no-op on non-Windows platforms.
func setNoWindow(cmd *exec.Cmd) {}
