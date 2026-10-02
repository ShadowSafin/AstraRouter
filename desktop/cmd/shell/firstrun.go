package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shadowsafin/astrarouter/desktop/internal/payload"
)

// payloadMarker records which embedded payload version was last extracted, so
// a new build re-materializes the runtime and an unchanged one does not.
const payloadMarker = ".payload-version"

// ensureRuntime writes the embedded runtime into root. It is a no-op when the
// shell was built without an embedded payload (a development or on-disk
// bundle) or when the marker already matches, and it repairs a partial
// extraction by overwriting on every mismatch.
func ensureRuntime(root string) error {
	if !payload.HasRuntime() {
		return nil
	}
	marker := filepath.Join(root, payloadMarker)
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == payload.Version() {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	fmt.Fprintf(os.Stderr, "astrarouter: extracting runtime to %s (first run)\n", root)
	if err := payload.Extract(root); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(payload.Version()+"\n"), 0o644)
}

// ensureFirstRunEnv makes the bundle runnable the moment it is opened: when
// native.env is absent it writes native.env and config.yaml from the bundled
// templates, with fresh secrets and free loopback ports. This is the same set
// of files install.ps1 writes, so a portable bundle and a per-user install
// agree, and neither requires the other to have run first.
//
// It never overwrites existing files: an installed app keeps its ports and
// secrets, and re-running is a no-op.
func ensureFirstRunEnv(root string) error {
	envPath := filepath.Join(root, "native.env")
	switch _, err := os.Stat(envPath); {
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return err
	}

	envTmpl, err := os.ReadFile(filepath.Join(root, "templates", "native.env.template"))
	if err != nil {
		return fmt.Errorf("no native.env and no template to create one (run install.ps1): %w", err)
	}
	cfgTmpl, err := os.ReadFile(filepath.Join(root, "templates", "config.yaml"))
	if err != nil {
		return fmt.Errorf("no native.env and no config template (run install.ps1): %w", err)
	}

	gwPort, err := freePort(18081)
	if err != nil {
		return err
	}
	dashPort, err := freePort(3100)
	if err != nil {
		return err
	}
	pgPort, err := freePort(5433)
	if err != nil {
		return err
	}
	admin, err := randomHex(24)
	if err != nil {
		return err
	}
	pgPassword, err := randomHex(16)
	if err != nil {
		return err
	}

	env := string(envTmpl)
	for k, v := range map[string]string{
		"__ADMIN_KEY__":   admin,
		"__GW_PORT__":     strconv.Itoa(gwPort),
		"__DASH_PORT__":   strconv.Itoa(dashPort),
		"__PG_PORT__":     strconv.Itoa(pgPort),
		"__PG_PASSWORD__": pgPassword,
	} {
		env = strings.ReplaceAll(env, k, v)
	}
	cfg := string(cfgTmpl)
	for k, v := range map[string]string{
		"__GW_PORT__":   strconv.Itoa(gwPort),
		"__DASH_PORT__": strconv.Itoa(dashPort),
		"__PG_PORT__":   strconv.Itoa(pgPort),
	} {
		cfg = strings.ReplaceAll(cfg, k, v)
	}

	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(cfg), 0o640); err != nil {
		return fmt.Errorf("write config.yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "astrarouter: first run — wrote native.env (gateway %d, dashboard %d, postgres %d)\n",
		gwPort, dashPort, pgPort)
	return nil
}

// freePort returns the first loopback port at or after start that can be bound.
func freePort(start int) (int, error) {
	for p := start; p < start+200; p++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			_ = ln.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free loopback port in %d..%d", start, start+199)
}

// randomHex returns n random bytes as hex, for generated secrets.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
