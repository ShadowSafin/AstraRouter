# Install: desktop app (Windows)

AstraRouter ships as **two separate programs**:

- **`AstraRouterSetup.exe`** — the setup program. Run it once to install.
- **`AstraRouter.exe`** — the app. What you open every day.

They are deliberately different: the installer installs and exits, and the app
runs the dashboard and backend. No Docker, no system database, no admin rights.

Prefer containers or systemd? Use the [Docker](docker.md) or
[Native](native.md) paths instead — they are separate and unaffected by this one.

## Install

1. Get the setup package (`AstraRouter-<version>-windows-x64.zip`) and unzip it
   anywhere. It contains `AstraRouterSetup.exe`, `AstraRouter.exe` and
   `install.ps1`.
2. Run **`AstraRouterSetup.exe`** (or `.\install.ps1`). The wizard guides you
   through:
   - a welcome and a one-line explanation of what AstraRouter does;
   - where to install it and where to keep its data (defaults to your user
     profile; you can choose another drive);
   - options: desktop shortcut, Start Menu entry, start at sign-in, and whether
     to launch when finished;
   - the administrator account — the single account that can sign in, stored as
     an Argon2id hash;
   - a review of your choices, then a progress screen.
3. The installer finishes and closes. AstraRouter opens (if you asked) or is
   available from its shortcut. Sign in with the administrator account you
   created — there are no default credentials.

To add a model provider afterwards, open Settings in the dashboard, or install
[Ollama](https://ollama.com) locally: the bundled `local-ollama` slot lights up
automatically once it serves `http://127.0.0.1:11434`.

### Unattended install

The installer can run without a window, which is useful for scripts and CI:

```powershell
@'
{
  "installDir": "$env:LOCALAPPDATA\\AstraRouter",
  "dataDir":    "$env:LOCALAPPDATA\\AstraRouter",
  "desktopShortcut": true,
  "startMenu": true,
  "startup": false,
  "launchAfter": false,
  "adminUser": "admin",
  "adminPassword": "a-strong-passphrase"
}
'@ | Set-Content install.json

.\AstraRouterSetup.exe --silent --config .\install.json
```

## Daily use

- Launch **`AstraRouter.exe`** from the shortcut. The splash tracks startup:
  local database → migrations → gateway → dashboard.
- Closing the window stops everything gracefully (requests drain, the database
  shuts down). Relaunching is fast: data persists under `data/`.
- The tray icon offers restart-services, open-logs-folder and quit.
- Logs live in `logs/` (`gateway.log`, `dashboard.log`, `migrate.log`,
  `shell.log`, `install.log`).

## Where things live

```text
<install folder>\
  AstraRouter.exe     the app (run this)
  uninstall.ps1       written by the installer
  root.txt            present only when the data folder is elsewhere

<data folder> (= the install folder unless you chose otherwise)
  bin\                gateway + node, installed by the setup program
  dashboard\          dashboard server
  data\postgres\      embedded database (binaries, cluster, cache)
  logs\               one file per process
  native.env          secrets + ports (owner-only file ACL)
  config.yaml         gateway config (loopback, traces off, ollama slot)
```

## Uninstall

Run `uninstall.ps1` from the install folder, or use Add/Remove Programs. The app
stops gracefully first. Your data is kept; pass `-PurgeData` to wipe the database
as well.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| "AstraRouter could not start" dialog | The path in the dialog points to `logs\shell-bootstrap.log`, which names the exact missing file. |
| App says it is not installed | Run the setup program (`AstraRouterSetup.exe`) first; the app only runs an installed copy. |
| Splash stuck on "Local database" | First launch downloads ~60 MB; wait. Persistent failure names the cause (no network, port taken, missing VC++ runtime — install it from the link in the message). |
| `AR_POSTGRES_PORT` clash | Edit `native.env`, set a free port, restart. |
| Installer setup fails | See `<data folder>\logs\install.log`; it records every step. |
| SmartScreen warning on first run | The executables are unsigned; choose "Run anyway". |

## Build it yourself

```powershell
cd desktop
.\build.ps1     # -> dist\AstraRouter.exe and dist\AstraRouterSetup.exe
```
