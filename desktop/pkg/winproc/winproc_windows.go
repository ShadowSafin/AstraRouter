//go:build windows

// Package winproc controls how child processes are created on Windows. The
// desktop app is a GUI-subsystem program, so every console child it starts
// (the gateway, node, postgres) would otherwise get its own console window and
// flash a terminal at the user. Hide marks a command to start silently.
package winproc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hide makes cmd run without a visible console window. It must be called before
// the command is started. CREATE_NO_WINDOW gives the child no console at all,
// which also stops it from flashing one on every crash-restart.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
}
