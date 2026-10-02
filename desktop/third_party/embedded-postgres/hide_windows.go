//go:build windows

package embeddedpostgres

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. It is defined here rather than imported
// from golang.org/x/sys to keep this fork free of extra dependencies.
const createNoWindow = 0x08000000

// hideWindow keeps the database binaries (pg_ctl, initdb) from opening a
// console window. The Synapass desktop app is a GUI process, so without
// this every embedded-postgres child would flash a command prompt at the
// user. Upstream exposes no hook for child creation flags, which is why this
// package is vendored under third_party and pinned to v1.34.0.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}
