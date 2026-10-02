# AstraRouter desktop (Windows) — two separate programs

AstraRouter ships as **two executables with different jobs**:

| Program | Purpose |
| --- | --- |
| `AstraRouterSetup.exe` | **Setup only.** Shows the wizard, installs everything, then exits. |
| `AstraRouter.exe` | **The app.** Runs the dashboard + backend in a WebView window. What you use every day. |

They are different products: the installer never becomes the app, and the app
never acts as an installer. Shortcuts point at `AstraRouter.exe`. The Docker
deployment is a separate path and is never touched.

```text
desktop/dist/
  AstraRouterSetup.exe   setup-only installer (~236 MB; embeds the app + runtime)
  AstraRouter.exe        standalone app (~19 MB; no installer code)
  install.ps1            convenience launcher for the installer
  README.md
```

## How they work

**Installer** (`cmd/installer`) carries the standalone app and its runtime
inside itself. On **Install** it:

1. installs the app executable and unpacks the runtime (gateway, portable Node,
   built dashboard, templates) into the app's data folder;
2. writes `native.env` + `config.yaml` with fresh secrets and free loopback
   ports (an existing config is preserved on reinstall);
3. starts the embedded PostgreSQL, applies migrations and creates the first
   administrator by calling the gateway's `native admin set`;
4. creates shortcuts and the Add/Remove Programs entry, optionally launches the
   app, then exits.

**App** (`cmd/app`) contains no installer. It reads `native.env`, starts the
embedded database, re-applies migrations (idempotent), supervises the gateway
and dashboard with crash recovery, and loads the dashboard in a WebView window.
Run it without an install and it says so plainly instead of half-installing.

## Layout

```text
desktop/
  cmd/app/                 the runtime app (AstraRouter.exe)
  cmd/installer/           the setup program (AstraRouterSetup.exe) + wizard UI
  internal/payload/        go:embed tree staged by build.ps1 (runtime + app exe)
  pkg/dbembed/             embedded PostgreSQL lifecycle, DSN handoff
  pkg/supervisor/          child processes, readiness, dotenv parsing
  assets/                  icon.png + icon.ico
  templates/               config.yaml + native.env.template
  build.ps1                builds both executables into dist/
  install.ps1              opens the installer
```

## Build & test

```powershell
# builds dist/AstraRouter.exe and dist/AstraRouterSetup.exe (Go + Node + network)
.\build.ps1

# desktop module tests
go test ./...            # from desktop/

# open the wizard
.\dist\AstraRouterSetup.exe

# unattended install (also how the pipeline is tested)
.\dist\AstraRouterSetup.exe --silent --config install.json

# headless end-to-end check of an installed app
.\AstraRouter.exe --smoke --root "$env:LOCALAPPDATA\AstraRouter"
```

`--smoke` starts everything, asserts the dashboard serves its page, shuts down
cleanly and exits 0/1. The installer's `--silent --config <file.json>` runs the
whole installation without a window, which is what CI uses.

## Data directory

```text
<install folder>\
  AstraRouter.exe     the standalone app
  uninstall.ps1       written by the installer
  root.txt            only when the data folder is elsewhere

<data folder> (= install folder by default)
  bin\                gateway + node
  dashboard\          dashboard server
  data\postgres\      embedded database (binaries, cluster, cache)
  logs\               one file per process + install.log
  native.env          secrets + ports (owner-only ACL)
  config.yaml         gateway config (loopback, traces off, ollama slot)
```
