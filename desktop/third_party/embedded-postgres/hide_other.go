//go:build !windows

package embeddedpostgres

import "os/exec"

// hideWindow is a no-op away from Windows: there is no console window to hide.
func hideWindow(cmd *exec.Cmd) {}
