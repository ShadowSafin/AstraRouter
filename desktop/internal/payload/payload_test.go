package payload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionNonEmpty(t *testing.T) {
	if Version() == "" {
		t.Fatal("Version() is empty; the ldflags stamp or the dev default is missing")
	}
}

// TestExtract exercises the extraction path. In a bare checkout only the
// placeholder is embedded, so this stays fast; a build with a staged runtime
// would extract ~200MB, which is why the heavy case is skipped.
func TestExtract(t *testing.T) {
	if HasRuntime() {
		t.Skip("a real runtime is staged; skipping the placeholder extraction check")
	}
	dest := t.TempDir()
	if err := Extract(dest); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".keep")); err != nil {
		t.Fatalf("placeholder not extracted: %v", err)
	}
}
