//go:build windows

// This binary is intentionally Windows-only: it uses WebView2, explorer.exe dialogs,
// and Windows registry APIs that have no cross-platform equivalent.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows/registry"

	"github.com/BaptisteTellier/autodeploy-desktop/internal/config"
	"github.com/BaptisteTellier/autodeploy-desktop/internal/job"
	"github.com/BaptisteTellier/autodeploy-desktop/internal/server"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// shortCommit returns the first 7 chars of the build commit SHA (or "").
func shortCommit() string {
	if len(commit) >= 7 {
		return commit[:7]
	}
	return commit
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("autodeploy-desktop %s (commit %s, built %s) starting", version, shortCommit(), date)

	// --- 1. Exe-relative paths -----------------------------------------------
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("resolve executable: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	// Allow env overrides so CI / advanced users can override any path.
	dataDir := envDefault("DATA_DIR", filepath.Join(exeDir, "data"))
	autodeployDir := envDefault("AUTODEPLOY_DIR", filepath.Join(exeDir, "autodeploy"))
	psExe := envDefault("PWSH_EXE", filepath.Join(exeDir, "pwsh", "pwsh.exe"))
	psScript := envDefault("PS_SCRIPT", "autodeploy.ps1")
	concurrency := envInt("WORKER_CONCURRENCY", 1)

	// --- 2. Augmented PATH ---------------------------------------------------
	// Prefix bundled bin/ (wsl shim), runtime/usr/bin (xorriso/rsync/bash) and
	// pwsh/ so every child process finds the right tools without needing any
	// system installation.
	augmentedPath := strings.Join([]string{
		filepath.Join(exeDir, "bin"),
		filepath.Join(exeDir, "runtime", "usr", "bin"),
		filepath.Join(exeDir, "pwsh"),
		os.Getenv("PATH"),
	}, string(os.PathListSeparator))

	// --- 3. Data layout + work dir -------------------------------------------
	if err := config.EnsureDataLayout(dataDir); err != nil {
		log.Fatalf("data layout: %v", err)
	}

	workDir := filepath.Join(dataDir, "work")
	_ = os.RemoveAll(workDir)
	_ = os.MkdirAll(workDir, 0o755)

	// --- 4. Job manager + store ----------------------------------------------
	store := config.NewStore(filepath.Join(dataDir, "configs"))

	mgr := job.NewManager(job.Options{
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		PSScript:      psScript,
		PSExe:         psExe,
		ExtraPath:     augmentedPath,
		MaxConcurrent: concurrency,
		KeepCompleted: 50,
	})

	// --- 5. HTTP server on ephemeral port ------------------------------------
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	log.Printf("listening on %s", baseURL)

	// quit channel: closed by /quit handler or window close
	quit := make(chan struct{})

	srv := server.New(server.Deps{
		Version:       version,
		Commit:        shortCommit(),
		BuildDate:     date,
		DataDir:       dataDir,
		AutodeployDir: autodeployDir,
		Store:         store,
		JobManager:    mgr,
		QuitFunc:      func() { safeClose(quit) },
	})

	httpSrv := &http.Server{
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	// --- 6. WebView2 window (with browser fallback) --------------------------
	// First do an explicit registry probe so we never attempt to load WebView2
	// when the Evergreen runtime is simply absent (avoids hard crashes in Run).
	if !webView2RuntimePresent() {
		log.Println("WebView2 Evergreen runtime not found — opening default browser")
		openBrowser(baseURL + "/")
		<-quit
	} else if !tryWebView2(baseURL+"/", quit) {
		// Runtime present but window creation / Run() failed unexpectedly.
		log.Println("WebView2 window failed — falling back to default browser")
		openBrowser(baseURL + "/")
		<-quit
	}

	// --- 7. Graceful shutdown ------------------------------------------------
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	mgr.Shutdown(ctx)
	log.Println("bye")
}

// webView2RuntimePresent checks the Windows registry for the WebView2 Evergreen
// runtime. It tries three well-known keys in order and returns true when it finds
// a non-empty "pv" value that is not "0.0.0.0".
func webView2RuntimePresent() bool {
	const guid = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	candidates := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + guid},
	}
	for _, c := range candidates {
		k, err := registry.OpenKey(c.root, c.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		k.Close()
		if err != nil {
			continue
		}
		if pv != "" && pv != "0.0.0.0" {
			log.Printf("WebView2 Evergreen runtime found (pv=%s)", pv)
			return true
		}
	}
	return false
}

// tryWebView2 creates the WebView2 window, runs the event loop, and destroys it.
// Returns true when everything completed normally, false on any failure (including
// a panic in NewWithOptions or Run). The caller should open the browser fallback
// when false is returned.
func tryWebView2(url string, quit chan struct{}) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WebView2 window error: %v", r)
			ok = false
		}
	}()
	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:  false,
		Window: nil,
	})
	if w == nil {
		log.Println("WebView2 NewWithOptions returned nil")
		return false
	}
	defer w.Destroy()

	w.SetTitle("autodeploy-desktop")
	w.SetSize(1280, 800, webview.HintNone)
	w.Navigate(url)

	// Bind a JS function so the window's close button also triggers graceful quit.
	_ = w.Bind("__desktopQuit", func() string {
		safeClose(quit)
		return ""
	})
	w.Init(`window.addEventListener('beforeunload', function(){ try{ __desktopQuit(); }catch(_){} });`)

	// Run blocks until the window is closed. Any panic here is caught by the
	// deferred recover above, which sets ok=false so the caller can fall back.
	w.Run()
	return true
}

// openBrowser opens url in the default Windows browser.
func openBrowser(url string) {
	if err := exec.Command("cmd", "/c", "start", url).Start(); err != nil {
		log.Printf("open browser: %v", err)
	}
}

// safeClose closes ch exactly once (ignores double-close).
func safeClose(ch chan struct{}) {
	defer func() { recover() }() //nolint:errcheck
	close(ch)
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
