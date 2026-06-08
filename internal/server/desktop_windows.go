//go:build windows

package server

import (
	"os/exec"

	"github.com/sqweek/dialog"
)

// pickISODialog opens the native Windows file-open dialog filtered to *.iso files.
// Returns the selected path or an error if cancelled / unavailable.
func pickISODialog() (string, error) {
	return dialog.File().Filter("ISO images", "iso").Title("Select Veeam Source ISO").Load()
}

// openExplorer opens dir in Windows Explorer.
func openExplorer(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}
