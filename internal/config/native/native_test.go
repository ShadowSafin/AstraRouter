package native

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultPathsAreComplete(t *testing.T) {
	p := DefaultPaths()
	for _, field := range []struct {
		name  string
		value string
	}{
		{"ConfigFile", p.ConfigFile},
		{"EnvFile", p.EnvFile},
		{"StateDir", p.StateDir},
		{"LogDir", p.LogDir},
		{"GatewayBin", p.GatewayBin},
		{"DashboardDir", p.DashboardDir},
		{"WorkerVenv", p.WorkerVenv},
	} {
		if field.value == "" {
			t.Fatalf("%s is empty", field.name)
		}
	}
}

func TestNativeRootRedirectsPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NATIVE_ROOT", root)
	p := DefaultPaths()
	for _, value := range []string{p.ConfigFile, p.EnvFile, p.StateDir, p.LogDir} {
		if filepath.Dir(value) != root && value != root {
			// ConfigFile/EnvFile sit directly in root; state and logs too.
			t.Fatalf("%q is not under NATIVE_ROOT %q", value, root)
		}
	}
	if err := p.EnsureStateDirs(); err != nil {
		t.Fatalf("EnsureStateDirs: %v", err)
	}
	for _, dir := range []string{p.StateDir, p.LogDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected directory %s: %v", dir, err)
		}
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	open := TCPCheck("open", ln.Addr().String(), 2*time.Second, "none")
	if !open.OK {
		t.Fatalf("expected open port to pass: %+v", open)
	}
	if open.Hint != "" {
		t.Fatalf("passing check must not carry a hint: %+v", open)
	}

	// A listener that is closed before the check is the most reliable closed
	// port: nothing else can win a race to bind it in between on loopback.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := closed.Addr().String()
	_ = closed.Close()

	shut := TCPCheck("shut", addr, 2*time.Second, "start the service")
	if shut.OK {
		t.Fatalf("expected closed port to fail: %+v", shut)
	}
	if shut.Hint == "" {
		t.Fatal("failing check must name the fix")
	}
}

func TestPortFreeCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	busyAddr := ln.Addr().String()
	defer ln.Close()

	if got := PortFreeCheck("busy", busyAddr, "free the port"); got.OK {
		t.Fatalf("expected bound port to fail: %+v", got)
	}

	freeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freeAddr := freeLn.Addr().String()
	_ = freeLn.Close()

	if got := PortFreeCheck("free", freeAddr, "free the port"); !got.OK {
		t.Fatalf("expected released port to pass: %+v", got)
	}
}

func TestExecutableCheck(t *testing.T) {
	if got := ExecutableCheck("missing", "astrarouter-definitely-not-a-binary", "install it"); got.OK {
		t.Fatalf("expected missing binary to fail: %+v", got)
	} else if got.Hint == "" {
		t.Fatal("failing check must name the fix")
	}
}

func TestDashboardBuildCheck(t *testing.T) {
	dir := t.TempDir()
	if got := DashboardBuildCheck(dir); got.OK {
		t.Fatalf("expected unbuilt dashboard to fail: %+v", got)
	}

	server := filepath.Join(dir, ".next", "standalone")
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(server, "server.js"), []byte("// stub"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := DashboardBuildCheck(dir); !got.OK {
		t.Fatalf("expected built dashboard to pass: %+v", got)
	}
}

func TestSplitNATSAddr(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"nats://localhost:4222", "localhost:4222", false},
		{"127.0.0.1:4222", "127.0.0.1:4222", false},
		{"nats://user:pass@example.com:4223", "example.com:4223", false},
		{"nats://localhost", "localhost:4222", false},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := SplitNATSAddr(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("SplitNATSAddr(%q): expected error, got %q", tt.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("SplitNATSAddr(%q): %v", tt.raw, err)
		}
		if got != tt.want {
			t.Fatalf("SplitNATSAddr(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
