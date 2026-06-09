//go:build windows

package job

import (
	"os/exec"
	"syscall"
)

// createNoWindow corresponds to the Win32 CREATE_NO_WINDOW process creation
// flag. It gives the child a console that has no window, so neither pwsh nor
// the tools it launches (the wsl shim, xorriso, rsync) flash a console window.
// Crucially, stdout/stderr piping still works — only the visible window is
// suppressed, unlike the GUI subsystem (which would detach stdio entirely).
const createNoWindow = 0x08000000

// setNoWindow makes cmd spawn without a visible console window.
func setNoWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
