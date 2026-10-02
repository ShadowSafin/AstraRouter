// Package dbembed runs a private PostgreSQL server inside the desktop
// bundle so the app never needs a system database install.
//
// The server lifecycle follows the process that starts it: Start blocks
// until postgres accepts connections (downloading the binaries on first
// use), and Stop shuts it down. Callers hand the returned DSN to the
// gateway through AR_POSTGRES_DSN, which wins over the config file, so the
// shared gateway binary needs no desktop-specific changes.
package dbembed

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// PostgresVersion pins the embedded server to the same major the Docker
// stack runs, so migrations and SQL behave identically on both paths.
const PostgresVersion = embeddedpostgres.V16

// Options configures one embedded server.
type Options struct {
	// Dir is the data root. pgdata, bin, rt and cache live underneath it,
	// so deleting Dir removes the database, the binaries and the cache.
	Dir string
	// Port is the loopback TCP port postgres listens on.
	Port uint32
	// User, Password and Database are created on first start.
	User     string
	Password string
	Database string
	// StartTimeout bounds the whole start, including the first-run binary
	// download. Zero means DefaultStartTimeout.
	StartTimeout time.Duration
	// Log receives the embedded library's chatter. Nil discards it; the
	// package still logs start/stop through Logger.
	Log io.Writer
}

// DefaultStartTimeout covers a first-run binary download on a slow link.
const DefaultStartTimeout = 15 * time.Minute

// Layout returns the directory tree Start creates under dir: the cluster
// data, the extracted runtime, and the download cache.
func Layout(dir string) (data, rt, cache string) {
	return filepath.Join(dir, "pgdata"),
		filepath.Join(dir, "rt"),
		filepath.Join(dir, "cache")
}

// DSN builds the loopback connection string for the given credentials. The
// password is percent-encoded so generated secrets always survive parsing.
func DSN(user, password string, port uint32, database string) string {
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     fmt.Sprintf("127.0.0.1:%d", port),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// Server is a running embedded postgres.
type Server struct {
	db  *embeddedpostgres.EmbeddedPostgres
	DSN string
	// Dir is the data root the server runs from.
	Dir string
}

// Start launches the embedded server, downloading and extracting the
// binaries into Dir on first use. It returns once postgres accepts
// connections, or an error naming the fix (port taken, no network for the
// first download, missing C++ runtime).
func Start(ctx context.Context, opts Options, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Dir == "" {
		return nil, fmt.Errorf("dbembed: data directory is empty")
	}
	if opts.Port == 0 {
		return nil, fmt.Errorf("dbembed: port is 0")
	}
	if opts.User == "" || opts.Database == "" {
		return nil, fmt.Errorf("dbembed: user and database are required")
	}
	timeout := opts.StartTimeout
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}
	data, rt, cache := Layout(opts.Dir)
	for _, dir := range []string{data, rt, cache} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("dbembed: create %s: %w", dir, err)
		}
	}
	libLog := opts.Log
	if libLog == nil {
		libLog = io.Discard
	}

	// BinariesPath stays unset on purpose: with no pre-downloaded binaries
	// the library fetches the archive into CachePath and extracts it into
	// RuntimePath, all under the data root.
	db := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Version(PostgresVersion).
			Username(opts.User).
			Password(opts.Password).
			Database(opts.Database).
			Port(opts.Port).
			DataPath(data).
			RuntimePath(rt).
			CachePath(cache).
			StartTimeout(timeout).
			Logger(libLog),
	)

	started := make(chan error, 1)
	go func() { started <- db.Start() }()
	select {
	case err := <-started:
		if err != nil {
			return nil, fmt.Errorf("dbembed: start postgres %s: %w", PostgresVersion, hint(err))
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("dbembed: start cancelled: %w", ctx.Err())
	}

	logger.Info("embedded postgres is ready",
		"version", string(PostgresVersion),
		"port", opts.Port,
		"dir", opts.Dir,
	)
	return &Server{db: db, DSN: DSN(opts.User, opts.Password, opts.Port, opts.Database), Dir: opts.Dir}, nil
}

// Stop shuts the server down. Closing pools first is the caller's job; Stop
// only stops the postgres process itself.
func (s *Server) Stop() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Stop(); err != nil {
		return fmt.Errorf("dbembed: stop: %w", err)
	}
	return nil
}

// hint translates common startup failures into actionable messages.
func hint(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "Only one usage of each socket address") ||
		// The library's own preflight reports this exact wording, so matching
		// only the OS messages left the most common failure unhinted.
		strings.Contains(msg, "process already listening on port"):
		return fmt.Errorf("%v (something already listens on this port: another copy of AstraRouter is running, "+
			"or a previous one was killed and left its database behind; quit it from the tray icon, "+
			"or stop the leftover database, then try again)", err)
	case strings.Contains(msg, "MSVCR") || strings.Contains(msg, "VCRUNTIME") || strings.Contains(msg, "0xc0000135"):
		return fmt.Errorf("%v (the Microsoft Visual C++ Redistributable is missing; install it from https://aka.ms/vs/17/release/vc_redist.x64.exe)", err)
	default:
		return err
	}
}
