// Package native describes a host installation of AstraRouter: where its files
// live, which variables configure it, and how to verify the deployment.
//
// It deliberately models the deployment only. The gateway configuration model
// (internal/config) and the application logic are shared unchanged between the
// Docker and native paths; this package adds the host-specific layer — file
// locations, env-file handling and preflight checks — on top.
package native

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	internalruntime "github.com/shadowsafin/astrarouter/internal/runtime"
)

// Paths locates every file a native installation reads or writes.
type Paths struct {
	// ConfigFile is the gateway YAML loaded through AR_CONFIG_FILE.
	ConfigFile string
	// EnvFile holds process environment (secrets included); mode 0600.
	EnvFile string
	// StateDir holds runtime state the supervisor and children own.
	StateDir string
	// LogDir receives child stdout/stderr when log files are configured.
	LogDir string
	// GatewayBin resolves the gateway executable. "astrarouter" means PATH.
	GatewayBin string
	// DashboardDir is the dashboard checkout (package.json lives here).
	DashboardDir string
	// WorkerVenv is the Python venv whose interpreter runs the workers.
	WorkerVenv string
}

// DefaultPaths returns the conventional locations for the current OS. NATIVE_ROOT
// redirects everything under one directory, which is what makes a user-local or
// evaluation install possible without touching system paths. NATIVE_GATEWAY_BIN,
// NATIVE_DASHBOARD_DIR and NATIVE_WORKER_VENV override individual entries.
func DefaultPaths() Paths {
	var p Paths
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		base = filepath.Join(base, "AstraRouter")
		p = Paths{
			ConfigFile:   filepath.Join(base, "config.yaml"),
			EnvFile:      filepath.Join(base, "native.env"),
			StateDir:     filepath.Join(base, "data"),
			LogDir:       filepath.Join(base, "logs"),
			WorkerVenv:   filepath.Join(base, "venv"),
			DashboardDir: filepath.Join(base, "dashboard"),
		}
	case "darwin":
		p = Paths{
			ConfigFile:   "/usr/local/etc/astrarouter/config.yaml",
			EnvFile:      "/usr/local/etc/astrarouter/native.env",
			StateDir:     "/usr/local/var/astrarouter",
			LogDir:       "/usr/local/var/log/astrarouter",
			WorkerVenv:   "/opt/astrarouter/venv",
			DashboardDir: "/opt/astrarouter/dashboard",
		}
	default:
		p = Paths{
			ConfigFile:   "/etc/astrarouter/config.yaml",
			EnvFile:      "/etc/astrarouter/native.env",
			StateDir:     "/var/lib/astrarouter",
			LogDir:       "/var/log/astrarouter",
			WorkerVenv:   "/opt/astrarouter/venv",
			DashboardDir: "/opt/astrarouter/dashboard",
		}
	}

	// A repository checkout runs from the source tree, not from system paths:
	// when started inside the repo, prefer the checkout's dashboard.
	if _, err := os.Stat(filepath.Join("dashboard", "package.json")); err == nil {
		if abs, err := filepath.Abs("dashboard"); err == nil {
			p.DashboardDir = abs
		} else {
			p.DashboardDir = "dashboard"
		}
	}

	if root := os.Getenv("NATIVE_ROOT"); root != "" {
		p.ConfigFile = filepath.Join(root, "config.yaml")
		p.EnvFile = filepath.Join(root, "native.env")
		p.StateDir = filepath.Join(root, "data")
		p.LogDir = filepath.Join(root, "logs")
	}
	if v := os.Getenv("NATIVE_GATEWAY_BIN"); v != "" {
		p.GatewayBin = v
	} else {
		p.GatewayBin = "astrarouter"
	}
	if v := os.Getenv("NATIVE_DASHBOARD_DIR"); v != "" {
		p.DashboardDir = v
	}
	if v := os.Getenv("NATIVE_WORKER_VENV"); v != "" {
		p.WorkerVenv = v
	}
	return p
}

// EnsureStateDirs creates the state and log directories.
func (p Paths) EnsureStateDirs() error {
	for _, dir := range []string{p.StateDir, p.LogDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// LoadEnvFile reads a KEY=VALUE file without touching the process environment,
// so callers can report what was found before applying anything.
func LoadEnvFile(path string) (map[string]string, error) {
	return internalruntime.LoadEnvFile(path)
}

// ApplyEnvFile loads path and applies its variables as defaults: anything
// already present in the environment wins, because an exported value is an
// explicit operator decision and the file holds installation defaults.
func ApplyEnvFile(path string) (map[string]string, error) {
	vars, err := LoadEnvFile(path)
	if err != nil {
		return nil, err
	}
	internalruntime.ApplyEnvDefaults(vars)
	return vars, nil
}

// Check is the result of one preflight probe.
type Check struct {
	// Name identifies the dependency ("postgres", "gateway-port").
	Name string
	// Target is what was probed ("127.0.0.1:5432").
	Target string
	// OK reports the probe outcome.
	OK bool
	// Detail carries success context (round-trip latency, resolved path).
	Detail string
	// Hint names the fix when OK is false. Every failing check must set one:
	// a red line without a next step is a dead end.
	Hint string
}

// TCPCheck dials address and reports reachability.
func TCPCheck(name, address string, timeout time.Duration, hint string) Check {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return Check{Name: name, Target: address, Hint: hint, Detail: err.Error()}
	}
	_ = conn.Close()
	return Check{Name: name, Target: address, OK: true, Detail: time.Since(start).Round(time.Millisecond).String()}
}

// PortFreeCheck reports whether address can be bound, i.e. the port the
// supervisor is about to use is not already taken.
func PortFreeCheck(name, address, hint string) Check {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return Check{Name: name, Target: address, Hint: hint, Detail: err.Error()}
	}
	_ = ln.Close()
	return Check{Name: name, Target: address, OK: true, Detail: "available"}
}

// ExecutableCheck resolves binary on PATH.
func ExecutableCheck(name, binary, hint string) Check {
	path, err := exec.LookPath(binary)
	if err != nil {
		return Check{Name: name, Target: binary, Hint: hint, Detail: err.Error()}
	}
	return Check{Name: name, Target: binary, OK: true, Detail: path}
}

// DashboardBuildCheck verifies the dashboard was built for production: the
// standalone server the supervisor launches must exist.
func DashboardBuildCheck(dashboardDir string) Check {
	server := filepath.Join(dashboardDir, ".next", "standalone", "server.js")
	if _, err := os.Stat(server); err != nil {
		return Check{
			Name:   "dashboard-build",
			Target: server,
			Hint:   "run `npm ci && npm run build` in " + dashboardDir + " (or `astrarouter native install`) first",
			Detail: err.Error(),
		}
	}
	return Check{Name: "dashboard-build", Target: server, OK: true, Detail: "standalone server present"}
}

// SplitNATSAddr reduces a NATS URL (nats://host:4222, possibly with credentials
// or a seed list) to a dialable host:port for the TCP preflight.
func SplitNATSAddr(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty NATS URL")
	}
	addr := raw
	if !containsScheme(raw) {
		addr = "nats://" + raw
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("parse NATS URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("NATS URL %q has no host", raw)
	}
	port := u.Port()
	if port == "" {
		port = "4222"
	}
	return net.JoinHostPort(host, port), nil
}

func containsScheme(raw string) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case ':':
			return i+1 < len(raw) && raw[i+1] == '/' && i+2 < len(raw) && raw[i+2] == '/'
		case '/':
			return false
		}
	}
	return false
}
