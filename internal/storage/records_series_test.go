package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/corerouter/corerouter/internal/domain"
)

// openTestPool connects to the database named by CR_TEST_POSTGRES_DSN, or
// skips the test when it is unset. Series bucketing can only be verified
// against real PostgreSQL: the bug it guards against is an epoch-alignment
// mismatch inside generate_series, which no mock reproduces.
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CR_TEST_POSTGRES_DSN is not set; skipping PostgreSQL-backed series test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test postgres: %v", err)
	}
	return pool
}

// TestUsageSeriesAlignsToEpochGrid is the regression test for the all-zero
// series bug: generate_series was aligned to the query's `from` timestamp
// while grouped rows were aligned to the epoch, so the join matched nothing
// and every bucket read zero while the summary stayed correct.
func TestUsageSeriesAlignsToEpochGrid(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	repos := NewRepositories(pool, nil)

	const prefix = "series-test-"
	now := time.Now().UTC()
	thisHour := now.Truncate(time.Hour)
	stamps := []time.Time{
		thisHour.Add(10 * time.Minute),
		thisHour.Add(20 * time.Minute),
		thisHour.Add(-30 * time.Minute),
	}
	for i, at := range stamps {
		rec := &domain.UsageRecord{
			RequestID: domain.RequestID(fmt.Sprintf("%s%d-%s", prefix, i, now.Format("150405"))),
			Outcome:   domain.OutcomeSuccess,
			CreatedAt: at,
		}
		if err := repos.Usage.Insert(ctx, rec); err != nil {
			t.Fatalf("insert usage record: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM usage_records WHERE request_id LIKE $1`, prefix+"%")
	})

	filter := QueryFilter{
		Search: prefix,
		From:   thisHour.Add(-2 * time.Hour),
		To:     thisHour.Add(2 * time.Hour),
	}

	series, err := repos.Usage.Series(ctx, filter, "1h")
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	var total int64
	nonzero := 0
	for _, b := range series {
		total += b.Requests
		if b.Requests > 0 {
			nonzero++
		}
	}
	if total != 3 {
		t.Fatalf("series total = %d, want 3 (buckets: %+v)", total, series)
	}
	if nonzero != 2 {
		t.Errorf("nonzero buckets = %d, want 2 (two distinct hours)", nonzero)
	}

	summary, err := repos.Usage.Summary(ctx, filter)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Requests != total {
		t.Errorf("summary requests = %d but series total = %d; the two queries disagree", summary.Requests, total)
	}
}
