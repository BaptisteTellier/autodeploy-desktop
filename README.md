# autodeploy-desktop

> Portable Windows desktop application for customising Veeam Software Appliance ISOs — no Docker, no WSL, no installation required.

[![CI](https://github.com/BaptisteTellier/autodeploy-desktop/actions/workflows/ci.yml/badge.svg)](https://github.com/BaptisteTellier/autodeploy-desktop/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

---

## What it does

`autodeploy-desktop` packages [`autodeploy.ps1`](https://github.com/BaptisteTellier/autodeploy) in a **single portable folder** for Windows. Extract the zip, double-click the exe, point to your Veeam source ISO, follow the wizard, and get a customised ISO in `data\output\`.

Everything needed to run is included in the zip:

- **autodeploy-desktop.exe** — local HTTP server + embedded WebView2 window.
- **pwsh/** — PowerShell 7.4.x portable (official Microsoft win-x64 zip, no installation needed).
- **runtime/** — MSYS2 portable minimal subtree: `xorriso.exe`, `rsync.exe`, `bash.exe`, `msys-2.0.dll`, and all transitive DLLs.
- **bin/wsl.exe** — Go shim that intercepts `wsl xorriso …` calls from the PS1 and forwards them to the bundled runtime.
- **autodeploy/autodeploy.ps1** — pinned copy of the PowerShell script baked into the bundle.
- **data/** — created on first launch: `iso/`, `output/`, `license/`, `conf/`, `configs/`, `work/`.

---

## Quick start

1. Download `autodeploy-desktop-<version>-win-x64.zip` from the [Releases](../../releases) page.
2. Extract to any folder (e.g. `C:\Tools\autodeploy-desktop\`).
3. Double-click **autodeploy-desktop.exe**.
4. The wizard opens. On step 1, click **Browse…** to point to your Veeam source ISO (15–20 GB — it is read in-place, not copied).
5. Fill in the configuration steps, click **Generate ISO**.
6. Watch the live build log. When the job completes, click **Open output folder** to find your customised ISO.

### SmartScreen note

The executable is not code-signed. Windows SmartScreen may show a warning on first launch:

> "Windows protected your PC."

Click **More info**, then **Run anyway**. This is expected for an unsigned portable app. The source code is fully open and the binary is built in CI — see [.github/workflows/build.yml](.github/workflows/build.yml).

### WebView2 note

The app uses the [WebView2](https://developer.microsoft.com/en-us/microsoft-edge/webview2/) runtime, which is bundled with Windows 11 and most Windows 10 installations. If WebView2 is absent, the app automatically falls back to opening your default browser on the local wizard URL (`http://127.0.0.1:<port>/`).

---

## Portable folder layout

```
autodeploy-desktop/
├─ autodeploy-desktop.exe      # main binary: HTTP server + WebView2 window
├─ WebView2Loader.dll          # WebView2 loader (optional — fallback to browser if absent)
├─ pwsh/                       # PowerShell 7.4.x portable (win-x64)
│  └─ pwsh.exe …
├─ runtime/                    # MSYS2 portable minimal (relocatable, no install needed)
│  ├─ usr/bin/xorriso.exe
│  ├─ usr/bin/rsync.exe
│  ├─ usr/bin/bash.exe
│  ├─ usr/bin/msys-2.0.dll
│  └─ etc/fstab                # maps C: → /mnt/c … Z: → /mnt/z (WSL convention)
├─ bin/
│  └─ wsl.exe                  # shim: intercepts `wsl <cmd>` → runtime/usr/bin/<cmd>
├─ autodeploy/
│  ├─ autodeploy.ps1           # pinned copy baked into this release
│  └─ .pinned-version          # tag/branch the PS1 was fetched from
└─ data/                       # created on first launch
   ├─ iso/                     # optional: drop source ISOs here instead of browsing
   ├─ output/                  # customised ISOs and .cfg files land here
   ├─ license/                 # optional: Veeam .lic files (for LicenseVBRTune)
   ├─ conf/                    # optional: restore config files (unattended.xml, .bco, …)
   ├─ configs/                 # saved JSON presets (survive app restarts)
   └─ work/                    # temporary staging (auto-purged on launch)
```

---

## Data folders

| Folder | Purpose |
|---|---|
| `data\iso\` | Optional: drop Veeam source ISOs here for the wizard's file picker. Large ISOs (15–20 GB) are read in-place; they are never copied. |
| `data\output\` | Each build job creates a subfolder with the customised ISO and `.cfg` files. |
| `data\license\` | Veeam `.lic` files — needed when *LicenseVBRTune* is enabled. |
| `data\conf\` | Restore config files: `unattended.xml`, `veeam_addsoconfpw.sh`, `conftoresto.bco`. |
| `data\configs\` | Named JSON presets saved from the UI. Drop PS1-compatible `.json` files here directly — they appear in the **Load preset** dropdown. |

---

## What it does not do

- **No network exposure.** The HTTP server binds strictly to `127.0.0.1`. There is no LAN toggle and no `inst.ks=` network kickstart feature (removed by design — desktop use only).
- **No authentication needed.** Because the server is localhost-only, it is accessible only to the logged-in user.
- **No job persistence across restarts.** In-memory job list is cleared on exit; output files on disk survive.

---

## Build from source

### Prerequisites

- Go 1.22+
- PowerShell 7 (for `build-bundle.ps1` and `fetch-runtime.ps1`)
- MSYS2 installed at `C:\msys64` (or set `$env:MSYS2_ROOT`) — needed only to build the `runtime/` subtree
- Internet access (to download pwsh zip and pacman packages)

### Quick build

```powershell
# Clone
git clone https://github.com/BaptisteTellier/autodeploy-desktop
cd autodeploy-desktop

# Full portable bundle (pwsh + MSYS2 runtime + autodeploy.ps1 + zip)
.\scripts\build-bundle.ps1

# Or via Make (if you have GNU make in your PATH):
make bundle
```

The zip lands in `dist\autodeploy-desktop-<version>-win-x64.zip`.

### Build steps in detail

| Step | What happens |
|---|---|
| `scripts\build-bundle.ps1` | Orchestrates all steps below |
| `go build ./cmd/autodeploy-desktop` | Compiles the main exe (GOOS=windows GOARCH=amd64) |
| `go build ./cmd/wslshim` | Compiles the `wsl.exe` shim |
| `scripts\fetch-runtime.ps1` | Downloads PowerShell 7.4.x zip and extracts into `pwsh/`; drives MSYS2 pacman to install xorriso + rsync, computes DLL closure via ntldd/ldd, copies the minimal subtree into `runtime/` |
| autodeploy.ps1 fetch | Downloads the pinned PS1 from GitHub into `autodeploy/` |
| Zip assembly | Assembles the layout above and produces the `.zip` in `dist/` |

### Individual targets

```powershell
# Go build only (no runtime bundling)
make build

# Download/update pwsh + MSYS2 runtime only
make fetch-runtime        # or: .\scripts\fetch-runtime.ps1

# Tests + quality checks
make test
make fmt
make vet
make i18n   # EN/FR translation key parity
make tidy
```

### Pinned versions

| Component | Pin location |
|---|---|
| PowerShell 7.4.x | `$PWSH_VERSION` in `scripts\fetch-runtime.ps1` (currently **7.4.10**) |
| MSYS2 packages | Latest from pacman at fetch time; pin by hardcoding `.pkg.tar.zst` URLs for reproducibility |
| autodeploy.ps1 | `-AutodeployVersion` parameter of `build-bundle.ps1` (default: `main`) |

---

## CI

GitHub Actions workflows:

| Workflow | Trigger | What it does |
|---|---|---|
| `ci.yml` | Push / PR | `gofmt`, `go vet`, `go test`, EN/FR i18n parity check |
| `build.yml` | Push to main/tags | Full bundle build on `windows-latest` + GitHub Release with SHA-256 |

---

## Acknowledgements

All ISO customisation logic (kickstart, GRUB, MFA, VCSP, license) is implemented by **[BaptisteTellier/autodeploy](https://github.com/BaptisteTellier/autodeploy)**. This project is the Windows desktop packaging.

## License

MIT — see [LICENSE](LICENSE).

*Made by Baptiste TELLIER for the Veeam community.*
