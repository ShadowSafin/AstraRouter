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
)

// ensureFirstRunEnv is a fallback for a runtime that has templates on disk but
// no configuration yet (for example a development bundle). A normal install
// already has native.env written by the installer, so this is a no-op there.
//
// It never overwrites existing files: an installed app keeps its ports and
// secrets.
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
		return fmt.Errorf("no native.env and no template to create one (run the AstraRouter installer): %w", err)
	}
	cfgTmpl, err := os.ReadFile(filepath.Join(root, "templates", "config.yaml"))
	if err != nil {
		return fmt.Errorf("no native.env and no config template (run the AstraRouter installer): %w", err)
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
