package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wizard inlines the console's typefaces as data URIs so it renders like
// the dashboard auth screens without a network request. This guards the
// embed/inject wiring.
func TestInstallerPageInlinesFonts(t *testing.T) {
	page := installerPage()

	if strings.Contains(page, "/*__FONT_FACE__*/") {
		t.Fatal("font placeholder was not replaced")
	}
	for _, want := range []string{
		"font-family:'Geist'",
		"font-family:'Geist Mono'",
		"font-family:'Instrument Serif'",
		"font-style:italic",
		"src:url(data:font/woff2;base64,",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("installer page missing %q", want)
		}
	}
	if n := strings.Count(page, "src:url(data:font/woff2;base64,"); n != 4 {
		t.Errorf("want 4 embedded fonts, got %d", n)
	}
}

// An upgrade must reuse the ports and secrets from the existing native.env.
// Returning a zeroed config made provisioning start postgres on port 0.
func TestLoadExistingConfig(t *testing.T) {
	dir := t.TempDir()
	env := `# AstraRouter desktop environment.
AR_ADMIN_KEY=abc123
AR_HTTP_ADDR=127.0.0.1:18081
PORT=3100
AR_POSTGRES_PORT=5433
AR_POSTGRES_USER=astrarouter
AR_POSTGRES_PASSWORD=secretpw
AR_POSTGRES_DB=astrarouter
`
	if err := os.WriteFile(filepath.Join(dir, "native.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadExistingConfig(dir, dir)
	if err != nil {
		t.Fatalf("loadExistingConfig: %v", err)
	}
	if cfg.pgPort != 5433 || cfg.gwPort != 18081 || cfg.dashPort != 3100 {
		t.Errorf("ports = pg %d, gw %d, dash %d; want 5433/18081/3100", cfg.pgPort, cfg.gwPort, cfg.dashPort)
	}
	if cfg.pgPassword != "secretpw" || cfg.adminKey != "abc123" {
		t.Errorf("secrets not reused: password=%q adminKey=%q", cfg.pgPassword, cfg.adminKey)
	}
}

func TestLoadExistingConfigRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "native.env"), []byte("AR_ADMIN_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExistingConfig(dir, dir); err == nil {
		t.Fatal("want error for a native.env without a postgres port/password")
	}
}
