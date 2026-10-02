# Install: desktop app (Windows)

AstraRouter as a real installed Windows application: **one self-contained
executable** plus a one-file installer. The `.exe` carries the gateway, a
portable Node runtime and the built dashboard inside it, extracts them on first
launch, starts an embedded PostgreSQL, and opens the dashboard in its own
window. No Docker, no database to install, no admin rights.

Prefer containers or systemd? Use the [Docker](docker.md) or
[Native](native.md) paths instead — they are separate and unaffected by this one.

## Install

1. Get the installer package (`AstraRouter-<version>-windows-x64.zip`) and unzip
   it anywhere. It contains `AstraRouter.exe`, `install.ps1` and `uninstall.ps1`.
2. Run `install.ps1` (double-click, or from PowerShell):
   ```powershell
   .\install.ps1
   ```
   It copies the single executable to `%LOCALAPPDATA%\AstraRouter`, extracts the
   embedded runtime, pre-downloads the embedded database, applies migrations,
   and creates Start Menu + Desktop shortcuts (and a run-at-sign-in entry).
3. AstraRouter opens in its own window. The first visit shows the setup screen
   that creates the console administrator — there are no default credentials.

To add a model provider afterwards, open Settings in the dashboard, or install
[Ollama](https://ollama.com) locally: the bundled `local-ollama` slot lights up
automatically once it serves `http://127.0.0.1:11434`.

## Why a single `.exe`

Everything the app needs to run travels inside `AstraRouter.exe`. The first
launch writes the runtime into the app's data directory and remembers the
version, so a new build re-extracts and an unchanged one does not. There is no
bundle folder to keep together and no separate runtime for the user to manage.

## Daily use

- Launch from the shortcut. The splash screen tracks startup: local database →
  migrations → gateway → dashboard.
- Closing the window stops everything gracefully (requests drain, the database
  shuts down). Relaunching is fast: data persists under `data/`.
- The tray icon offers restart-services, open-logs-folder and quit.
- Logs live in `logs/` (`gateway.log`, `dashboard.log`, `migrate.log`,
  `shell.log`; early startup failures go to `shell-bootstrap.log`).

## Where things live

```text
%LOCALAPPDATA%\AstraRouter\
  AstraRouter.exe   the single-file app (runtime embedded)
  bin\              gateway + node, extracted from the .exe on first run
  dashboard\        dashboard server, extracted from the .exe on first run
  data\postgres\    embedded database (binaries, cluster, cache)
  logs\             one file per process
  native.env        secrets + ports (owner-only file ACL)
  config.yaml       gateway config (loopback, traces off, ollama slot)
```

## Uninstall

Run `uninstall.ps1` from the install dir, or use Add/Remove Programs. The app
stops gracefully first. Your data is kept in a temp folder and its path is
printed; pass `-PurgeData` to wipe the database as well.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| "AstraRouter could not start" dialog | Read the path in the dialog: `logs\shell-bootstrap.log` names the exact missing file or failure. |
| Splash stuck on "Local database" | First launch downloads ~60 MB; wait. Persistent failure names the cause (no network, port taken, missing VC++ runtime — install it from the link in the message). |
| `AR_POSTGRES_PORT` clash | Edit `native.env`, set a free port, restart. The app probes free ports on first run; later software can still collide. |
| Error page after retry | Read the named log in `logs/`; most gateway misconfigurations print the exact bad value. |
| SmartScreen warning on first run | The app is unsigned; choose "Run anyway". |
| Dashboard shows setup again | `data/` was deleted or moved — the console admin lives in the embedded database. |
| Need a provider key | Dashboard → Settings → Providers. Keys are sealed with the install's key material and stay local. |

## Build it yourself

```powershell
cd desktop
.\build.ps1     # -> desktop\dist\AstraRouter.exe  (single self-contained app)
```
