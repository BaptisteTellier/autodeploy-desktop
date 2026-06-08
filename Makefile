.PHONY: build bundle fetch-runtime test fmt vet tidy clean i18n

VERSION            ?= dev
AUTODEPLOY_VERSION ?= main

# ── Build (Go only, no runtime bundling) ─────────────────────────────────────
# Compiles the desktop exe only — useful for quick iteration.
# For a full portable zip (with pwsh + MSYS2 runtime), use `make bundle` instead.
build:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	    go build \
	    -ldflags="-s -w -X main.version=$(VERSION)" \
	    -o autodeploy-desktop.exe \
	    ./cmd/autodeploy-desktop

# ── Portable bundle (exe + pwsh + MSYS2 runtime + autodeploy.ps1) ─────────────
# Requires PowerShell 7 on PATH and MSYS2 installed (for the runtime subtree).
# Set MSYS2_ROOT if MSYS2 is not at C:\msys64.
bundle:
	pwsh -NoProfile -ExecutionPolicy Bypass \
	    -File scripts/build-bundle.ps1 \
	    -Version $(VERSION) \
	    -AutodeployVersion $(AUTODEPLOY_VERSION)

# ── Portable runtime only (pwsh + MSYS2 subtree) ─────────────────────────────
fetch-runtime:
	pwsh -NoProfile -ExecutionPolicy Bypass \
	    -File scripts/fetch-runtime.ps1

# ── Tests ─────────────────────────────────────────────────────────────────────
test:
	go test ./... -race -count=1

# ── Code quality ──────────────────────────────────────────────────────────────
fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy

# ── i18n parity check (EN vs FR) ─────────────────────────────────────────────
i18n:
	python3 scripts/check-i18n.py

# ── Clean ─────────────────────────────────────────────────────────────────────
clean:
	rm -rf bin dist autodeploy-desktop.exe
