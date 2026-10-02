// Package payload holds what the setup program ships inside its own executable:
// the AstraRouter gateway, a portable Node runtime, the built dashboard, the
// config templates, and the standalone app executable it installs. `build.ps1`
// stages them under files/ before compiling cmd/installer, so
// AstraRouterSetup.exe is self-contained.
//
// Only the installer embeds this. The standalone app (cmd/app) never does: it
// runs the runtime the installer unpacked. In a bare checkout the staged tree
// is only a placeholder and HasRuntime reports false.
package payload

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:files
var files embed.FS

// version is the payload build stamp, injected by build.ps1 through
// -ldflags -X. The shell re-extracts whenever it differs from the marker left
// by a previous launch. "dev" (a plain `go build`) means no stamped runtime.
var version = "dev"

// Version is the build stamp of the embedded payload.
func Version() string { return strings.TrimSpace(version) }

// HasRuntime reports whether a real runtime tree is embedded, as opposed to
// the placeholder that ships in a source checkout.
func HasRuntime() bool {
	_, err := fs.Stat(files, "files/bin/astrarouter.exe")
	return err == nil
}

// Template returns one of the embedded config templates (for example
// "native.env.template" or "config.yaml"). The installer uses these to write a
// fresh installation's configuration without duplicating the template text.
func Template(name string) ([]byte, error) {
	return files.ReadFile("files/templates/" + name)
}

// appExeName is the standalone runtime executable staged at the root of the
// payload. It is installed next to the runtime, never extracted as part of it.
const appExeName = "AstraRouter.exe"

// AppExecutable returns the standalone application executable that this
// installer installs. The installer owns the copy; the runtime app never
// embeds or extracts itself.
func AppExecutable() ([]byte, error) {
	return files.ReadFile("files/" + appExeName)
}

// Extract writes the embedded runtime under dest, preserving the bundle
// layout (bin/, dashboard/, assets/, templates/). Extraction is additive:
// existing files are overwritten, so it doubles as a repair.
func Extract(dest string) error {
	return fs.WalkDir(files, "files", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("files", filepath.FromSlash(p))
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// The application executable is installed separately (see
		// AppExecutable), not unpacked with the runtime.
		if d.IsDir() == false && rel == appExeName {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}
