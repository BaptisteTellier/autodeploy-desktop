//go:build windows

// wslshim is a Windows shim that replaces the real wsl.exe in the bundle's
// bin\ directory (which is prepended to PATH ahead of the system wsl.exe).
//
// It reproduces the behaviour of scripts/wsl-wrapper.sh from autodeploy-web:
//
//   - WSL management flags (--version, --list/-l, --status, --shutdown, etc.)
//     are answered with no-op success responses so that autodeploy.ps1
//     environment probes pass without a real WSL installation.
//
//   - wsl [--cd <dir>] [-e|--exec] <cmd> [args…] and bare wsl <cmd> [args…]
//     strip the leading wsl and any wsl-only flags, then exec
//     <bundleRoot>\runtime\usr\bin\<cmd>.exe with MSYS2 environment variables
//     set so that the MSYS2 runtime resolves /mnt/<drive> paths correctly
//     (the fstab mount mapping is configured by the fstab workstream; we only
//     need to export the right environment variables here).
//
// bundleRoot resolution: os.Executable() lives at <bundle>\bin\wsl.exe, so
// bundleRoot = filepath.Dir(filepath.Dir(executable)).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// wsl-wrapper.sh: if no arguments, print usage and exit 0.
	if len(args) == 0 {
		fmt.Println("WSL shim (autodeploy-desktop). Forwards calls to MSYS2 binaries.")
		fmt.Println("Usage: wsl <command> [args...]")
		return 0
	}

	// wsl-wrapper.sh: handle WSL management flags as no-ops.
	switch args[0] {
	case "--version":
		// wsl-wrapper.sh: echo "WSL version (container shim): 1.0"
		fmt.Println("WSL version (autodeploy-desktop shim): 2.0.0")
		fmt.Println("WSL Kernel Version: 5.15.0")
		fmt.Println("WSLg version: 1.0.0")
		return 0

	case "--list", "-l":
		// wsl-wrapper.sh: echo "(container shim — no distros, native exec)"
		fmt.Println("(autodeploy-desktop shim — no WSL distros, running via MSYS2)")
		return 0

	case "--status":
		fmt.Println("Default Version: 2")
		return 0

	case "--set-default-version":
		// No-op: accept the call (autodeploy.ps1 may probe WSL version setup).
		return 0

	case "--shutdown", "--terminate", "--unregister", "--install", "--update":
		// wsl-wrapper.sh: these are all no-ops in the shim.
		return 0
	}

	// Strip wsl-only flags before the actual command.
	// Supported wsl invocation forms (from wsl --help):
	//   wsl [--cd <dir>] [-e | --exec] <cmd> [args…]
	//   wsl <cmd> [args…]
	//
	// We consume --cd <dir> (updating working directory for the child process)
	// and -e / --exec (which just means "run this command directly").
	// Anything else before a non-flag token is treated as the command start.
	cmdArgs, cwd := stripWslFlags(args)

	if len(cmdArgs) == 0 {
		// wsl-wrapper.sh: bare flags with no command → silent exit 0.
		return 0
	}

	// Resolve the MSYS2 binary path: bundleRoot\runtime\usr\bin\<cmd>.exe
	runtimeBin, err := resolveRuntimeBin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wsl shim: %v\n", err)
		return 1
	}

	cmdName := cmdArgs[0]
	cmdExe := filepath.Join(runtimeBin, cmdName+".exe")

	// Build child command.
	//nolint:gosec // cmdExe is constructed from a bundle-controlled directory.
	child := exec.Command(cmdExe, cmdArgs[1:]...)

	// Forward stdio so callers (autodeploy.ps1 piping to the process) work correctly.
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	// Set working directory if --cd was provided; otherwise inherit.
	if cwd != "" {
		child.Dir = cwd
	}

	// Build the MSYS2 environment.
	// wsl-wrapper.sh runs inside a container that is already a POSIX env, so
	// it does exec "$@" directly. On Windows we must set the MSYS2 env vars
	// to get the same effect: MSYS2 needs MSYSTEM and its own bin on PATH,
	// and CHERE_INVOKING suppresses the automatic cd to $HOME.
	child.Env = buildMsys2Env(runtimeBin)

	if err := child.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "wsl shim: exec %q: %v\n", cmdExe, err)
		return 1
	}
	return 0
}

// stripWslFlags removes wsl-specific flags from the argument list and returns
// (remaining args, cwd).
//
// Consumed flags:
//
//	--cd <dir>   – set child working directory (Windows or POSIX path)
//	-e, --exec   – "execute the following command directly" (no-op for us)
//	--           – end of wsl flags
func stripWslFlags(args []string) ([]string, string) {
	var cwd string
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--cd":
			// --cd requires the next argument as the directory.
			if i+1 < len(args) {
				cwd = args[i+1]
				i += 2
			} else {
				i++
			}
		case "-e", "--exec":
			// Signals that the rest are the command; consume the flag and stop.
			i++
			return args[i:], cwd
		case "--":
			// Explicit end-of-wsl-flags marker.
			i++
			return args[i:], cwd
		default:
			// First non-flag token is the command name.
			return args[i:], cwd
		}
	}
	return nil, cwd
}

// resolveRuntimeBin returns the absolute path to the MSYS2 runtime bin
// directory: <bundleRoot>\runtime\usr\bin.
//
// Layout:
//
//	<bundleRoot>\
//	  bin\wsl.exe          ← os.Executable()
//	  runtime\usr\bin\     ← MSYS2 binaries (xorriso.exe, rsync.exe, bash.exe …)
func resolveRuntimeBin() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	// Follow any symlinks so we get the real path.
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("eval symlinks: %w", err)
	}
	// exe = <bundle>\bin\wsl.exe
	// binDir  = <bundle>\bin
	// bundleRoot = <bundle>
	bundleRoot := filepath.Dir(filepath.Dir(exe))
	runtimeBin := filepath.Join(bundleRoot, "runtime", "usr", "bin")
	return runtimeBin, nil
}

// buildMsys2Env constructs the environment for the MSYS2 child process.
//
// Key variables (mirroring what wsl-wrapper.sh gets for free by running inside
// a Linux container):
//
//   - MSYSTEM=MSYS    – selects the MSYS sub-system (POSIX compatibility layer).
//   - PATH            – prepend runtimeBin so MSYS2 tools find each other.
//   - CHERE_INVOKING=1 – tells bash not to cd to $HOME on startup.
//   - MSYS=winsymlinks:nativestrict – optional; improves symlink handling.
//
// The /mnt/<drive> path resolution relies on runtime\etc\fstab (configured by
// the fstab workstream); no extra env is needed here for that mapping.
func buildMsys2Env(runtimeBin string) []string {
	env := os.Environ()

	// Prepend runtimeBin to PATH so MSYS2 DLLs and tools resolve correctly.
	newPath := runtimeBin
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			newPath = runtimeBin + string(os.PathListSeparator) + e[len("PATH="):]
			break
		}
	}

	// Build the new env, replacing/adding the variables we care about.
	result := make([]string, 0, len(env)+4)
	for _, e := range env {
		upper := strings.ToUpper(e)
		switch {
		case strings.HasPrefix(upper, "PATH="),
			strings.HasPrefix(upper, "MSYSTEM="),
			strings.HasPrefix(upper, "CHERE_INVOKING="),
			strings.HasPrefix(upper, "MSYS="):
			// We will add these ourselves below; skip the inherited value.
			continue
		}
		result = append(result, e)
	}

	result = append(result,
		"PATH="+newPath,
		"MSYSTEM=MSYS",
		"CHERE_INVOKING=1",
		"MSYS=winsymlinks:nativestrict",
	)
	return result
}
