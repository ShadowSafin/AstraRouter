package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native.env")
	content := "# comment\nexport AR_ADMIN_KEY=secret\nPORT=3100\nQUOTED=\"a b\"\nSINGLE='c d'\nEMPTY=\nBROKENLINE\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"AR_ADMIN_KEY": "secret",
		"PORT":         "3100",
		"QUOTED":       "a b",
		"SINGLE":       "c d",
		"EMPTY":        "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["BROKENLINE"]; ok {
		t.Error("lines without '=' must be ignored")
	}
	if _, ok := got["# comment"]; ok {
		t.Error("comments must be ignored")
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "nope.env")); err == nil {
		t.Error("missing env file must return an error")
	}
}
