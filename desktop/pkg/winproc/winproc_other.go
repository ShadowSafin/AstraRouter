//go:build !windows

package winproc

import "os/exec"

// Hide is a no-op away from Windows; there is no console window to suppress.
func Hide(cmd *exec.Cmd) {}
