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

// The wizard renders before the runtime is extracted, so the brand mark has to
// travel inside the executable.
func TestInstallerPageInlinesBrandIcon(t *testing.T) {
	page := installerPage()
	if strings.Contains(page, "__BRAND_ICON__") {
		t.Fatal("brand icon placeholder was not replaced")
	}
	if !strings.Contains(page, `src="data:image/png;base64,`) {
		t.Fatal("brand icon is not an embedded data URI")
	}
	if len(installerBrand) == 0 {
		t.Fatal("brand.png is empty")
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

// Known Folder Move puts the desktop under OneDrive, so %USERPROFILE%\Desktop
// is not it. The registry value arrives with environment variables still
// unexpanded, and a shortcut must never be built for a path that is not there.
func TestResolveDesktop(t *testing.T) {
	dir := t.TempDir()

	if got := resolveDesktop(dir); got != dir {
		t.Errorf("existing folder: got %q, want %q", got, dir)
	}

	t.Setenv("AR_TEST_DESKTOP", dir)
	if got := resolveDesktop(`%AR_TEST_DESKTOP%`); got != dir {
		t.Errorf("unexpanded env var: got %q, want %q", got, dir)
	}

	if got := resolveDesktop(filepath.Join(dir, "redirected-away")); got != "" {
		t.Errorf("missing folder: got %q, want empty", got)
	}
	if got := resolveDesktop("   "); got != "" {
		t.Errorf("blank: got %q, want empty", got)
	}
	if got := resolveDesktop(`%NOT_SET_ANYWHERE%`); got != "" {
		t.Errorf("unresolvable env var: got %q, want empty", got)
	}
}

// desktopDir must resolve a real folder on this machine, or the wizard silently
// creates no desktop shortcut.
func TestDesktopDirResolvesAFolder(t *testing.T) {
	dir := desktopDir()
	if dir == "" {
		t.Skip("no resolvable desktop folder in this environment")
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("desktopDir() = %q, which is not a folder (err %v)", dir, err)
	}
	t.Logf("desktop folder: %s", dir)
}
