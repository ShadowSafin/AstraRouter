module github.com/shadowsafin/astrarouter/desktop

go 1.27.1

// The embedded-postgres library spawns pg_ctl and initdb with exec.Command
// and gives no hook for Windows child-creation flags, so those processes open
// a console window. third_party/embedded-postgres is a pinned copy (v1.34.0)
// patched to pass CREATE_NO_WINDOW; see its hide_windows.go.
replace github.com/fergusstrange/embedded-postgres => ./third_party/embedded-postgres

require (
	github.com/fergusstrange/embedded-postgres v1.34.0
	github.com/getlantern/systray v1.2.2
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	golang.org/x/sys v0.48.0
)

require (
	github.com/getlantern/context v0.0.0-20190109183933-c447772a6520 // indirect
	github.com/getlantern/errors v0.0.0-20190325191628-abdb3e3e36f7 // indirect
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7 // indirect
	github.com/getlantern/hex v0.0.0-20190417191902-c6586a6fe0b7 // indirect
	github.com/getlantern/hidden v0.0.0-20190325191715-f02dbb02be55 // indirect
	github.com/getlantern/ops v0.0.0-20190325191751-d70cb0d6f85f // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/oxtoacart/bpool v0.0.0-20190530202638-03653db5a59c // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
)
