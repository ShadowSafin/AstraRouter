package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test: it re-executes the test binary as a
// portable dummy child process. GO_WANT_HELPER_PROCESS=1 selects helper mode;
// without it the function returns immediately and the "test" passes.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("GO_HELPER_MODE") {
	case "exit":
		code, _ := strconv.Atoi(os.Getenv("GO_HELPER_CODE"))
		os.Exit(code)
	case "echo":
		fmt.Println("helper was here")
		os.Exit(0)
	case "sleep":
		select {}
	default:
		os.Exit(2)
	}
}

// helperArgv builds a command line that re-runs the test binary in helper mode.
// An explicit -test.timeout arms the testing framework's watchdog: without it
// the flag defaults to no timeout, and a helper that blocks forever (sleep)
// trips the runtime's deadlock detector instead of simply blocking.
func helperArgv(t *testing.T, mode, code string) ([]string, []string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{bin, "-test.run=TestHelperProcess", "-test.timeout=30s"},
		[]string{
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_MODE=" + mode,
			"GO_HELPER_CODE=" + code,
		}
}

// waitFor polls Status until name reaches want or the timeout elapses.
func waitFor(t *testing.T, sup *Supervisor, name string, want State, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, st := range sup.Status() {
			if st.Name == name && st.State == want {
				return st
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %s did not reach %s within %s (status: %+v)", name, want, timeout, sup.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runSupervised runs the supervisor to completion with a hard timeout so a
// regression fails the test instead of hanging CI.
func runSupervised(t *testing.T, sup *Supervisor, ctx context.Context) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("supervisor did not return within 30s")
		return nil
	}
}

func TestSupervisorCleanExit(t *testing.T) {
	argv, env := helperArgv(t, "exit", "0")
	sup := New([]Process{{Name: "one-shot", Argv: argv, Env: env, Policy: RestartNever}}, nil)
	if err := runSupervised(t, sup, context.Background()); err != nil {
		t.Fatalf("clean exit should succeed: %v", err)
	}
}

func TestSupervisorFailedExit(t *testing.T) {
	argv, env := helperArgv(t, "exit", "1")
	sup := New([]Process{{
		Name: "failing", Argv: argv, Env: env,
		Policy: RestartNever,
	}}, nil)
	err := runSupervised(t, sup, context.Background())
	if err == nil || !strings.Contains(err.Error(), "failing") {
		t.Fatalf("expected an error naming the process, got: %v", err)
	}
}

func TestSupervisorRestartsThenGivesUp(t *testing.T) {
	argv, env := helperArgv(t, "exit", "3")
	sup := New([]Process{{
		Name: "flapping", Argv: argv, Env: env,
		Policy: RestartOnFailure, MaxRestarts: 2, RestartDelay: 10 * time.Millisecond,
	}}, nil)
	err := runSupervised(t, sup, context.Background())
	if err == nil || !strings.Contains(err.Error(), "flapping") {
		t.Fatalf("expected a terminal error naming the process, got: %v", err)
	}
	states := sup.Status()
	if len(states) != 1 || states[0].Restarts != 2 || states[0].State != StateFailed {
		t.Fatalf("expected 2 restarts then failed, got %+v", states)
	}
}

func TestSupervisorStopsOnCancel(t *testing.T) {
	argv, env := helperArgv(t, "sleep", "0")
	sup := New([]Process{{
		Name: "sleeper", Argv: argv, Env: env,
		Policy: RestartOnFailure, MaxRestarts: 10, RestartDelay: time.Hour,
		StopTimeout: 5 * time.Second,
	}}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	st := waitFor(t, sup, "sleeper", StateRunning, 10*time.Second)
	if st.PID <= 0 {
		t.Fatalf("expected a real PID, got %+v", st)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled supervision should return nil, got: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("supervisor did not stop after cancel")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("shutdown took too long: %s", elapsed.Round(time.Second))
	}
}

func TestSupervisorCapturesLogs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	argv, env := helperArgv(t, "echo", "0")
	sup := New([]Process{{
		Name: "talker", Argv: argv, Env: env,
		Policy: RestartNever, StdoutPath: out, StderrPath: filepath.Join(dir, "err.log"),
	}}, nil)
	if err := runSupervised(t, sup, context.Background()); err != nil {
		t.Fatalf("clean exit should succeed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "helper was here") {
		t.Fatalf("log does not contain child output: %q", data)
	}
}

func TestWaitForReady(t *testing.T) {
	t.Run("immediate 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		if err := WaitForReady(context.Background(), srv.URL, 5*time.Second); err != nil {
			t.Fatalf("expected ready, got: %v", err)
		}
	})

	t.Run("redirect to a live page counts as ready", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		// The dashboard answers 307 to /login without a session; the client
		// follows it to 200, which is what proves the server is up.
		if err := WaitForReady(context.Background(), srv.URL, 5*time.Second); err != nil {
			t.Fatalf("expected ready after redirect, got: %v", err)
		}
	})

	t.Run("eventual 200", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		if err := WaitForReady(context.Background(), srv.URL, 5*time.Second); err != nil {
			t.Fatalf("expected eventual readiness, got: %v", err)
		}
	})

	t.Run("unreachable times out", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		// Port 1 is unconnectable everywhere without root trickery.
		if err := WaitForReady(ctx, "http://127.0.0.1:1/", 5*time.Second); err == nil {
			t.Fatal("expected an error for an unreachable server, got nil")
		}
	})
}
