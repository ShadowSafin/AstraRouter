package dbembed

import (
	"strings"
	"testing"
)

func TestDSNEncodesCredentials(t *testing.T) {
	dsn := DSN("synapass", "p@ss:word", 5433, "synapass")
	for _, want := range []string{
		"postgres://",
		"synapass:p%40ss%3Aword@",
		"127.0.0.1:5433",
		"/synapass",
		"sslmode=disable",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q is missing %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "p@ss:word") {
		t.Errorf("password must be percent-encoded, got %q", dsn)
	}
}

func TestLayoutKeepsEverythingUnderDir(t *testing.T) {
	data, rt, cache := Layout(`C:\app\data\postgres`)
	for _, dir := range []string{data, rt, cache} {
		if !strings.HasPrefix(dir, `C:\app\data\postgres`) {
			t.Errorf("layout leaked outside the data root: %q", dir)
		}
	}
	if len(map[string]bool{data: true, rt: true, cache: true}) != 3 {
		t.Errorf("layout directories must be distinct: %q %q %q", data, rt, cache)
	}
}

func TestStartRefusesEmptyOptions(t *testing.T) {
	if _, err := Start(t.Context(), Options{}, nil); err == nil {
		t.Error("Start with empty options must fail, not start a database nowhere")
	}
	if _, err := Start(t.Context(), Options{Dir: t.TempDir()}, nil); err == nil {
		t.Error("Start with port 0 must fail")
	}
}
