package job

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Runner executes one invocation of autodeploy.ps1.
type Runner struct {
	AutodeployDir string // where the PS1 lives, e.g. /opt/autodeploy
	PSScript      string // autodeploy.ps1
	// PSExe is the absolute path to the pwsh binary. Falls back to "pwsh" when empty.
	PSExe string
	// ExtraPath is prepended to PATH so bundled tools are found first.
	ExtraPath  string
	IsoDir     string // /data/iso — where source ISOs live
	OutputDir  string // /data/output  (job subfolder created here)
	LicenseDir string // /data/license
	ConfDir    string // /data/conf
	JobID      string // per-job output subfolder: OutputDir/JobID/
	// SourceISO is the absolute path to the source ISO as stored in the PS1
	// config. The PS1 performs the Copy-Item itself using this path, so the
	// runner never needs to stage the ISO. (Manager.Submit ensures this is
	// always absolute before writing the config JSON.)
	SourceISO string
	WorkDir   string // base dir for per-job staging dirs, e.g. /data/work

	// OverrideScript, if non-empty and the file exists, is used instead of
	// AutodeployDir/PSScript. Populated from /data/autodeploy/autodeploy.ps1
	// when the user triggers a runtime update from the admin page.
	OverrideScript string

	// ConfigPath is the absolute path to the JSON the PS1 reads.
	ConfigPath string

	// OnLine receives each captured stdout/stderr line, scrubbed.
	OnLine func(string)
}

// Run launches pwsh and blocks until completion. Returns the process exit
// code (or -1 on spawn failure) and any I/O error.
// Each job gets its own fresh staging dir under WorkDir/<jobID>; the whole
// dir is removed via defer after the run, giving clean per-job isolation.
func (r *Runner) Run(ctx context.Context) (int, error) {
	if r.OnLine == nil {
		r.OnLine = func(string) {}
	}

	scriptPath := filepath.Join(r.AutodeployDir, r.PSScript)
	// Prefer runtime override when it exists.
	if r.OverrideScript != "" {
		if _, err := os.Stat(r.OverrideScript); err == nil {
			scriptPath = r.OverrideScript
		}
	}
	if _, err := os.Stat(scriptPath); err != nil {
		return -1, fmt.Errorf("autodeploy.ps1 not found at %s: %w", scriptPath, err)
	}
	if _, err := os.Stat(r.ConfigPath); err != nil {
		return -1, fmt.Errorf("config file missing: %w", err)
	}

	// Per-job staging directory under WorkDir/<jobID>.
	// cwd is always stageDir: the config carries an absolute SourceISO so the
	// PS1 finds the source regardless of cwd, and the PS1 writes the output ISO
	// (bare filename from OutputISO) into cwd — i.e. stageDir — where
	// collectOutputs will pick it up.
	stageDir := filepath.Join(r.WorkDir, r.JobID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return -1, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// We do NOT copy or symlink the source ISO: the PS1 config already contains
	// the absolute path to the ISO and the PS1 copies it itself (Copy-Item).

	// Make companion directories (license, conf) reachable from stageDir under
	// their bare names, as the PS1 references them by name relative to cwd via
	// Add-FolderToISO. We avoid os.Symlink (requires admin or Developer Mode on
	// Windows) and use a directory junction instead, with a recursive-copy fallback.
	for _, entry := range []struct{ Name, Source string }{
		{"license", r.LicenseDir},
		{"conf", r.ConfDir},
	} {
		if entry.Source == "" {
			continue
		}
		dst := filepath.Join(stageDir, entry.Name)
		if err := linkOrCopyDir(entry.Source, dst); err != nil {
			r.OnLine(fmt.Sprintf("[warn] stage %s dir: %v", entry.Name, err))
		}
	}

	// Stage the PS1 into cwd so the PS1 can reference files with bare names.
	stagedScript := filepath.Join(stageDir, r.PSScript)
	if err := copyFile(scriptPath, stagedScript); err != nil {
		return -1, fmt.Errorf("stage script: %w", err)
	}

	// Stage the config under a hidden name to avoid collisions.
	stagedConfig := filepath.Join(stageDir, ".job-"+filepath.Base(r.ConfigPath))
	if err := copyFile(r.ConfigPath, stagedConfig); err != nil {
		return -1, fmt.Errorf("stage config: %w", err)
	}

	// These ephemeral files must not be collected as job outputs.
	skipSet := map[string]bool{
		filepath.Base(stagedScript): true,
		filepath.Base(stagedConfig): true,
	}

	// Resolve pwsh binary — prefer bundled PSExe, fall back to system PATH.
	psExe := r.PSExe
	if psExe == "" {
		psExe = "pwsh"
	}

	// The PS1 writes all outputs (customized ISO, grub.cfg, vbr-ks.cfg, logs)
	// into its cwd. cwd is stageDir, so collectOutputs finds them all there.
	args := []string{
		"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", stagedScript,
		"-ConfigFile", stagedConfig,
	}

	cmd := exec.CommandContext(ctx, psExe, args...)
	cmd.Dir = stageDir
	// Spawn pwsh without a console window (Windows). Children it launches — the
	// wsl shim, xorriso, rsync — inherit this windowless console, so nothing
	// flashes on screen while stdout/stderr remain piped to us.
	setNoWindow(cmd)

	// Build child environment: prepend augmented PATH and disable telemetry.
	env := os.Environ()
	if r.ExtraPath != "" {
		// Replace existing PATH entry with the augmented one.
		const pathKey = "PATH"
		found := false
		for i, e := range env {
			if strings.HasPrefix(strings.ToUpper(e), pathKey+"=") {
				env[i] = pathKey + "=" + r.ExtraPath
				found = true
				break
			}
		}
		if !found {
			env = append(env, pathKey+"="+r.ExtraPath)
		}
	}
	env = append(env, "POWERSHELL_TELEMETRY_OPTOUT=1")
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}

	r.OnLine(fmt.Sprintf("[%s] $ %s %s", time.Now().Format(time.RFC3339), psExe, strings.Join(args, " ")))
	r.OnLine(fmt.Sprintf("[%s] cwd: %s", time.Now().Format(time.RFC3339), stageDir))

	if err := cmd.Start(); err != nil {
		return -1, err
	}

	go r.consume(stdout)
	go r.consume(stderr)

	err = cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil // exit code reported separately
		} else {
			exit = -1
		}
	}

	// Move all regular files in the staging dir (excluding staged inputs) to
	// OutputDir/JobID/. defer os.RemoveAll(stageDir) cleans the rest.
	if r.JobID != "" {
		r.collectOutputs(stageDir, skipSet)
	}

	return exit, err
}

// collectOutputs moves every regular file in stageDir (not in skip, not a
// symlink, not a directory) into OutputDir/JobID/. Because the staging dir is
// per-job and freshly created, every regular file that wasn't staged by us IS
// a job output. defer os.RemoveAll(stageDir) in the caller handles cleanup.
func (r *Runner) collectOutputs(stageDir string, skip map[string]bool) {
	jobOut := filepath.Join(r.OutputDir, r.JobID)
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		r.OnLine(fmt.Sprintf("[output-error] mkdir %s: %v", jobOut, err))
		return
	}

	entries, err := os.ReadDir(stageDir)
	if err != nil {
		r.OnLine(fmt.Sprintf("[output-error] readdir %s: %v", stageDir, err))
		return
	}
	for _, e := range entries {
		name := e.Name()
		if skip[name] {
			continue
		}
		// Skip symlinks and directories.
		linfo, lerr := os.Lstat(filepath.Join(stageDir, name))
		if lerr != nil || linfo.Mode()&os.ModeSymlink != 0 || linfo.IsDir() {
			continue
		}

		src := filepath.Join(stageDir, name)
		dst := filepath.Join(jobOut, name)
		if mvErr := os.Rename(src, dst); mvErr != nil {
			// Cross-device fallback (different mount points).
			if cpErr := copyFile(src, dst); cpErr == nil {
				_ = os.Remove(src)
				r.OnLine(fmt.Sprintf("[output] %s", name))
			} else {
				r.OnLine(fmt.Sprintf("[output-error] %s: %v", name, cpErr))
			}
		} else {
			r.OnLine(fmt.Sprintf("[output] %s", name))
		}
	}
}

// consume reads a pipe line by line, scrubs secrets, forwards to OnLine.
func (r *Runner) consume(p io.Reader) {
	scanner := bufio.NewScanner(p)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		r.OnLine(scrub(scanner.Text()))
	}
}

// linkOrCopyDir makes src reachable at dst without using os.Symlink.
// On Windows, os.Symlink requires Administrator privileges or Developer Mode;
// a directory junction (mklink /J) works for any user on NTFS.
// The function tries a junction first; if mklink fails (e.g. non-NTFS, old
// Windows, or permission issue) it falls back to a recursive copy.
// A no-op is performed when src does not exist or is empty.
func linkOrCopyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // source absent — nothing to do
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}

	// Skip staging an empty source directory — Add-FolderToISO checks
	// Test-Path and returns early when the folder is absent, so an empty
	// directory and a missing one are equivalent from the PS1's perspective.
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	// Try a Windows directory junction first (no admin required).
	out, jErr := exec.Command("cmd", "/c", "mklink", "/J", dst, src).CombinedOutput()
	if jErr == nil {
		return nil
	}
	// Junction failed — log and fall back to a recursive copy.
	_ = out // ignore verbose mklink output; errors are checked via jErr

	return copyDirRecursive(src, dst)
}

// copyDirRecursive recursively copies all files from src into dst, creating
// dst and any sub-directories as needed.
func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// scrub masks anything that looks like a Veeam password or MFA secret.
var (
	rePwd    = regexp.MustCompile(`(?i)(password\s*[:=]\s*)("?[^"\s,;]+)`)
	reMfa    = regexp.MustCompile(`(?i)(mfasecretkey\s*[:=]\s*)("?[A-Z2-7]{16,32})`)
	reToken  = regexp.MustCompile(`(?i)(recoverytoken\s*[:=]\s*)("?[0-9a-f-]{36})`)
	reVCSPpw = regexp.MustCompile(`(?i)(VCSPPassword\s*[:=]\s*)("?[^"\s,;]+)`)
)

func scrub(s string) string {
	s = rePwd.ReplaceAllString(s, `${1}***`)
	s = reMfa.ReplaceAllString(s, `${1}***`)
	s = reToken.ReplaceAllString(s, `${1}***`)
	s = reVCSPpw.ReplaceAllString(s, `${1}***`)
	return s
}
