// Package supervisor runs the desktop stack: the gateway, the dashboard
// node server, and anything else the app needs, with crash recovery and
// graceful shutdown. It is deliberately separate from the server-oriented
// native supervisor so the desktop path never depends on server tooling.
package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shadowsafin/synapass/desktop/pkg/winproc"
)

// Child is one supervised process.
type Child struct {
	// Name appears in logs and the tray status.
	Name string
	// Argv is the executable plus arguments.
	Argv []string
	// Dir is the working directory. Empty means inherit.
	Dir string
	// Env holds extra KEY=value pairs appended to the current environment.
	Env []string
	// StdoutPath and StderrPath capture output. Empty discards it.
	StdoutPath string
	StderrPath string
	// RestartDelay waits this long before restarting a crashed child.
	RestartDelay time.Duration
	// StopTimeout bounds graceful shutdown before the process is killed.
	StopTimeout time.Duration
	// MaxRestarts caps consecutive crash restarts; exceeding it fails the
	// whole stack so a crash loop surfaces instead of spinning silently.
	MaxRestarts int
}

// proc is a Child plus its live process handle.
type proc struct {
	child Child
	cmd   *exec.Cmd
	done  chan error
}

// Supervisor starts children in order and stops them newest-first.
type Supervisor struct {
	children []Child
	logger   *slog.Logger
}

// New builds a supervisor for children started in slice order.
func New(children []Child, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{children: children, logger: logger}
}

// Run starts every child and blocks until the context is cancelled or a
// child exhausts its restarts. It always stops everything it started.
func (s *Supervisor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	running := make([]*proc, 0, len(s.children))
	defer func() {
		for i := len(running) - 1; i >= 0; i-- {
			stopProc(running[i].cmd, running[i].child.StopTimeout, s.logger, running[i].child.Name)
		}
	}()

	for _, c := range s.children {
		if c.RestartDelay <= 0 {
			c.RestartDelay = 3 * time.Second
		}
		if c.StopTimeout <= 0 {
			c.StopTimeout = 30 * time.Second
		}
		if c.MaxRestarts <= 0 {
			c.MaxRestarts = 5
		}
		p := &proc{child: c, done: make(chan error, 1)}
		if err := startProc(p, s.logger); err != nil {
			return fmt.Errorf("supervisor: start %s: %w", c.Name, err)
		}
		running = append(running, p)
	}

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		for _, p := range running {
			select {
			case err := <-p.done:
				if ctx.Err() != nil {
					return nil
				}
				if err := s.recover(ctx, p, err); err != nil {
					return err
				}
			default:
			}
		}
	}
}

// recover restarts a crashed child with backoff, giving up after MaxRestarts
// consecutive failures so a crash loop becomes a visible error page.
func (s *Supervisor) recover(ctx context.Context, p *proc, cause error) error {
	s.logger.Warn("child exited unexpectedly; restarting",
		"child", p.child.Name, "error", cause, "delay", p.child.RestartDelay)
	for restarts := 1; ; restarts++ {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(p.child.RestartDelay):
		}
		if err := startProc(p, s.logger); err != nil {
			s.logger.Error("child restart failed", "child", p.child.Name, "error", err)
		} else {
			return nil
		}
		if restarts >= p.child.MaxRestarts {
			return fmt.Errorf("supervisor: %s crashed %d times in a row; see %s",
				p.child.Name, restarts+1, logName(p.child))
		}
	}
}

// startProc launches the child process and reports its exit on p.done.
func startProc(p *proc, logger *slog.Logger) error {
	if len(p.child.Argv) == 0 {
		return fmt.Errorf("no command")
	}
	cmd := exec.Command(p.child.Argv[0], p.child.Argv[1:]...)
	// The app is a GUI process, so a console child would flash a terminal. Every
	// supervised process is a console program (gateway, node), so hide them all.
	winproc.Hide(cmd)
	if p.child.Dir != "" {
		cmd.Dir = p.child.Dir
	}
	cmd.Env = append(os.Environ(), p.child.Env...)
	stdout, err := openLog(p.child.StdoutPath)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := openLog(p.child.StderrPath)
	if err != nil {
		return err
	}
	defer stderr.Close()
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	logger.Info("child started", "child", p.child.Name, "pid", cmd.Process.Pid)
	p.cmd = cmd
	p.done = make(chan error, 1)
	go func() { p.done <- cmd.Wait() }()
	return nil
}

// openLog opens a log file for appending, creating parents. Empty means nil.
func openLog(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
}

func logName(c Child) string {
	if c.StdoutPath != "" {
		return c.StdoutPath
	}
	return "the logs directory"
}

// stopProc asks the process to exit, then kills it after the timeout.
func stopProc(cmd *exec.Cmd, timeout time.Duration, logger *slog.Logger, name string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.Info("child stopped", "child", name)
	case <-time.After(timeout):
		logger.Warn("child ignored shutdown; killing", "child", name)
		_ = cmd.Process.Kill()
	}
}

// WaitForHTTP polls url until it answers 2xx/3xx or the timeout lapses. Any
// 2xx or 3xx counts: the dashboard answers a redirect to /login while it is
// perfectly healthy, and the gateway answers 503 until its providers resolve.
func WaitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return nil
			}
			last = fmt.Errorf("GET %s: %s", url, resp.Status)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for %s: %v", url, last)
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// FetchTitle GETs url and returns its HTML title, for smoke checks.
func FetchTitle(ctx context.Context, url string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	m := titleRe.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("no <title> in %s (status %s)", url, resp.Status)
	}
	return strings.TrimSpace(string(m[1])), nil
}

// LoadEnvFile parses KEY=value lines, tolerating `export ` prefixes, quotes
// and comments. It returns the pairs without touching the process env.
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') ||
			(val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out, sc.Err()
}
