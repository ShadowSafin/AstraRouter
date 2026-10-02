package runtime

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// WaitForReady polls url until it answers successfully or the timeout elapses.
//
// Any 2xx or 3xx counts as ready: the dashboard answers a redirect to /login
// when no session exists, which still proves the server is up. Anything else —
// connection refused, timeout, 5xx — keeps polling, because during startup the
// only interesting question is "is it up yet", not "what exactly is wrong".
func WaitForReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build readiness request for %s: %w", url, err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s: %v", url, timeout.Round(time.Second), lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
