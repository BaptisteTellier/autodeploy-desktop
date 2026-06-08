//go:build windows

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
	wv := tryWebView2(baseURL+"/", quit)
	if wv != nil {
		// WebView2 available — run the event loop (blocks until window closes).
		wv.Run()
		wv.Destroy()
	} else {
		// Fallback: open default browser, then wait for /quit or OS signal.
		log.Println("WebView2 runtime not available — opening default browser")
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

// tryWebView2 attempts to create a WebView2 window. Returns nil if the runtime
// is absent (panics from go-webview2 are recovered).
func tryWebView2(url string, quit chan struct{}) (wv webview.WebView) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WebView2 unavailable: %v", r)
			wv = nil
		}
	}()
	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:  false,
		Window: nil,
	})
	if w == nil {
		return nil
	}
	w.SetTitle("autodeploy-desktop")
	w.SetSize(1280, 800, webview.HintNone)
	w.Navigate(url)

	// Bind a JS function so the window's close button also triggers graceful quit.
	_ = w.Bind("__desktopQuit", func() string {
		safeClose(quit)
		return ""
	})
	w.Init(`window.addEventListener('beforeunload', function(){ try{ __desktopQuit(); }catch(_){} });`)
	return w
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
