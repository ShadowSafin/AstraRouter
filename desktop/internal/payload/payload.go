// Package payload holds the runtime the desktop app ships inside its own
// executable: the AstraRouter gateway, a portable Node runtime, the built
// dashboard, and the config templates. `build.ps1` stages them under files/
// before compiling cmd/shell, so the deliverable is one self-contained
// AstraRouter.exe rather than a folder bundle.
//
// In a bare checkout the staged tree is only a placeholder, HasRuntime reports
// false, and the shell runs from files on disk instead — which keeps a plain
// `go build ./cmd/shell` working for development.
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
