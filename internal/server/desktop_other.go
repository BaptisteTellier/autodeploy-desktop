//go:build !windows

package server

import "errors"

// pickISODialog is not available on non-Windows platforms.
func pickISODialog() (string, error) {
	return "", errors.New("file dialog not supported on this platform")
}

// openExplorer is not available on non-Windows platforms.
func openExplorer(dir string) error {
	return errors.New("explorer not supported on this platform")
}
