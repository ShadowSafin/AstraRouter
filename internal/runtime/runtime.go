// Package runtime supervises the AstraRouter processes on a host that has no
// container orchestrator.
//
// It is the native counterpart to the Docker Compose service definitions: the
// same three processes (gateway, dashboard, workers) with the same restart and
// shutdown semantics, expressed as a library so `astrarouter native up` behaves
// identically on Linux, macOS and Windows. Anything systemd-specific (unit
// files, sandboxing, journal logging) lives in deploy/systemd, not here.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// RestartPolicy decides what happens when a supervised process exits on its own.
type RestartPolicy int

const (
	// RestartNever treats any exit as terminal: exit 0 means the process is
	// done, anything else fails the whole supervisor. Used for one-shot steps.
	RestartNever RestartPolicy = iota
	// RestartOnFailure restarts a process that exits non-zero, up to MaxRestarts.
	// A clean exit still ends supervision successfully.
	RestartOnFailure
)

// Process describes one supervised child process.
type Process struct {
	// Name identifies the process in logs and status output.
	Name string
	// Argv is the command line; Argv[0] is the executable.
	Argv []string
	// Dir is the working directory. Empty inherits the supervisor's.
	Dir string
	// Env holds extra KEY=VALUE pairs appended to the supervisor's environment.
	Env []string
	// StdoutPath and StderrPath append child output to files. Empty inherits
	// the supervisor's own streams, which is what makes `native up` readable
	// in a terminal; production deployments set both so logs survive restarts.
	StdoutPath string
	StderrPath string
	// Policy and its bounds. MaxRestarts caps unexpected-exit restarts; zero or
	// negative means restart without a cap, which is what a foreground
	// supervisor wants — the operator watches the terminal and Ctrl-C stops it.
	Policy      RestartPolicy
	MaxRestarts int
	// RestartDelay waits between attempts. Zero defaults to 2 seconds.
	RestartDelay time.Duration
	// StopTimeout bounds graceful shutdown before SIGKILL. Zero defaults to
	// 30 seconds, which must exceed the gateway's own drain timeout.
	StopTimeout time.Duration
}

// State is the lifecycle state of one supervised process.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Status is a point-in-time snapshot of one supervised process.
type Status struct {
	Name     string
	State    State
	PID      int
	Restarts int
	// Uptime since the current attempt started. Zero when not running.
	Uptime time.Duration
}

// Supervisor starts a set of processes, restarts the ones that fail, and stops
// everything — newest first — when the context ends or any process fails
// terminally. The zero value is not usable; use New.
type Supervisor struct {
	logger *slog.Logger
	procs  []Process

	mu     sync.Mutex
	states map[string]*procState
}

type procState struct {
	status  Status
	cmd     *exec.Cmd
	started time.Time
	stdout  *os.File
	stderr  *os.File
	ownsOut bool
	ownsErr bool
}

// New builds a supervisor for the given processes. Names must be unique.
func New(procs []Process, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{logger: logger, procs: procs, states: map[string]*procState{}}
}

// Run starts every process and blocks until the context ends or a process fails
// terminally. A context cancelled by the caller (SIGINT/SIGTERM handling lives
// with the caller) stops everything gracefully and returns nil; any other
// terminal event returns an error naming the process that caused it.
func (s *Supervisor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := range s.procs {
		if err := s.start(&s.procs[i]); err != nil {
			s.stopAll()
			return fmt.Errorf("start %s: %w", s.procs[i].Name, err)
		}
	}

	// One slot per process; a supervision loop reports exactly once, when its
	// process reaches a terminal state. The channel is buffered so a loop never
	// blocks on reporting, even if nobody is collecting anymore.
	done := make(chan procResult, len(s.procs))
	for i := range s.procs {
		go s.supervise(ctx, &s.procs[i], done)
	}

	// The first terminal report decides the outcome; the rest are stopped and
	// collected so no child is left behind. A caller-cancelled context (SIGINT,
	// SIGTERM) reports a clean shutdown; a failed child reports its error.
	received := 0
	var firstErr error
	for received < len(s.procs) {
		select {
		case <-ctx.Done():
			s.stopAll()
			for received < len(s.procs) {
				if res := <-done; res.err != nil && firstErr == nil {
					firstErr = res.err
				}
				received++
			}
			return firstErr
		case res := <-done:
			received++
			if res.err != nil && firstErr == nil {
				firstErr = res.err
			}
			if received < len(s.procs) {
				cancel()
				s.stopAll()
			}
		}
	}
	return firstErr
}

type procResult struct {
	name string
	err  error
}

// supervise runs one process until it reaches a terminal state.
func (s *Supervisor) supervise(ctx context.Context, p *Process, done chan<- procResult) {
	restarts := 0
	for {
		err := s.wait(ctx, p)
		if err == nil {
			// Clean exit or context end: terminal either way.
			s.setState(p.Name, func(st *procState) {
				st.status.State = StateStopped
				st.cmd = nil
			})
			s.closeLogs(p.Name)
			done <- procResult{name: p.Name}
			return
		}

		// An unexpected exit while the supervisor is still wanted.
		if ctx.Err() != nil {
			s.setState(p.Name, func(st *procState) {
				st.status.State = StateStopped
				st.cmd = nil
			})
			s.closeLogs(p.Name)
			done <- procResult{name: p.Name}
			return
		}

		if p.Policy != RestartOnFailure || (p.MaxRestarts > 0 && restarts >= p.MaxRestarts) {
			s.setState(p.Name, func(st *procState) {
				st.status.State = StateFailed
				st.cmd = nil
			})
			s.closeLogs(p.Name)
			done <- procResult{name: p.Name, err: fmt.Errorf("%s exited unexpectedly: %w", p.Name, err)}
			return
		}

		restarts++
		s.setState(p.Name, func(st *procState) {
			st.status.Restarts = restarts
			st.cmd = nil
		})
		s.logger.Warn("process exited, restarting", "process", p.Name, "attempt", restarts, "error", err)

		delay := p.RestartDelay
		if delay <= 0 {
			delay = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			s.setState(p.Name, func(st *procState) {
				st.status.State = StateStopped
			})
			s.closeLogs(p.Name)
			done <- procResult{name: p.Name}
			return
		case <-time.After(delay):
		}

		if err := s.start(p); err != nil {
			s.setState(p.Name, func(st *procState) { st.status.State = StateFailed })
			s.closeLogs(p.Name)
			done <- procResult{name: p.Name, err: fmt.Errorf("restart %s: %w", p.Name, err)}
			return
		}
	}
}

// wait blocks until the current attempt of p ends: either the process exits or
// the context ends (in which case it is stopped first). A nil return means the
// process exited cleanly or was stopped on request.
func (s *Supervisor) wait(ctx context.Context, p *Process) error {
	s.mu.Lock()
	st, ok := s.states[p.Name]
	s.mu.Unlock()
	if !ok || st.cmd == nil {
		return fmt.Errorf("no running attempt")
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- st.cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = s.stopAttempt(p)
		<-waitCh
		return nil
	case err := <-waitCh:
		return err
	}
}

// start launches one attempt of p. The caller must not have a running attempt.
func (s *Supervisor) start(p *Process) error {
	if len(p.Argv) == 0 {
		return fmt.Errorf("empty command line")
	}
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	if p.Dir != "" {
		cmd.Dir = p.Dir
	}
	cmd.Env = append(os.Environ(), p.Env...)

	stdout, ownsOut, err := openLog(p.StdoutPath, os.Stdout)
	if err != nil {
		return fmt.Errorf("open stdout log: %w", err)
	}
	stderr, ownsErr, err := openLog(p.StderrPath, os.Stderr)
	if err != nil {
		if ownsOut {
			_ = stdout.Close()
		}
		return fmt.Errorf("open stderr log: %w", err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		if ownsOut {
			_ = stdout.Close()
		}
		if ownsErr {
			_ = stderr.Close()
		}
		return err
	}

	s.mu.Lock()
	prev, restarted := s.states[p.Name]
	restarts := 0
	if restarted {
		restarts = prev.status.Restarts
	}
	s.states[p.Name] = &procState{
		status:  Status{Name: p.Name, State: StateRunning, PID: cmd.Process.Pid, Restarts: restarts},
		cmd:     cmd,
		started: time.Now(),
		stdout:  stdout,
		stderr:  stderr,
		ownsOut: ownsOut,
		ownsErr: ownsErr,
	}
	s.mu.Unlock()
	s.logger.Info("process started", "process", p.Name, "pid", cmd.Process.Pid)
	return nil
}

// stopAll stops every running attempt, newest first, so dependents (dashboard,
// workers) stop before the gateway they talk to. Processes are expected to be
// listed in startup order.
func (s *Supervisor) stopAll() {
	for i := len(s.procs) - 1; i >= 0; i-- {
		_ = s.stopAttempt(&s.procs[i])
	}
}

// stopAttempt gracefully stops the current attempt of p, if any.
func (s *Supervisor) stopAttempt(p *Process) error {
	s.mu.Lock()
	st, ok := s.states[p.Name]
	s.mu.Unlock()
	if !ok || st.cmd == nil || st.cmd.Process == nil {
		return nil
	}
	proc := st.cmd.Process

	timeout := p.StopTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// SIGTERM is the graceful path everywhere the gateway runs; on platforms
	// where the signal does not exist (Windows) the call fails and shutdown
	// falls through to SIGKILL immediately rather than waiting out the timeout
	// for a signal that can never arrive.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		_ = proc.Kill()
		return nil
	}

	done := make(chan struct{})
	go func() {
		_, _ = proc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = proc.Kill()
		<-done
	}
	return nil
}

// setState mutates the status of p under lock.
func (s *Supervisor) setState(name string, fn func(*procState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[name]; ok {
		fn(st)
	}
}

// closeLogs closes log files the supervisor opened.
func (s *Supervisor) closeLogs(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[name]; ok {
		if st.ownsOut {
			_ = st.stdout.Close()
		}
		if st.ownsErr {
			_ = st.stderr.Close()
		}
	}
}

// Status returns a snapshot of every known process. Unknown processes (never
// started) are omitted.
func (s *Supervisor) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.states))
	for _, name := range s.procOrder() {
		st := s.states[name]
		status := st.status
		if status.State == StateRunning {
			status.Uptime = time.Since(st.started).Round(time.Second)
		}
		out = append(out, status)
	}
	return out
}

// procOrder returns supervised names in startup order for stable output.
func (s *Supervisor) procOrder() []string {
	names := make([]string, 0, len(s.procs))
	for _, p := range s.procs {
		if _, ok := s.states[p.Name]; ok {
			names = append(names, p.Name)
		}
	}
	return names
}

// openLog opens path for appending, creating parent directories as needed. An
// empty path inherits stream instead, and reports no ownership so the caller
// never closes a shared descriptor.
func openLog(path string, stream *os.File) (*os.File, bool, error) {
	if path == "" {
		return stream, false, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}
