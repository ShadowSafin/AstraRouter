package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/corerouter/corerouter/internal/config"
	"github.com/corerouter/corerouter/internal/domain"
)

// Subject conventions.
//
// Subjects are hierarchical and versioned:
//
//	cr.<domain>.<event>[.<qualifier>]
//
// The leading version segment is absent because a breaking payload change is
// expressed by adding a new subject rather than by mutating an existing one; a
// durable consumer keeps reading the old subject while the new one warms up.
const (
	// SubjectUsageRecorded carries a completed request's usage record.
	SubjectUsageRecorded = "cr.usage.recorded"
	// SubjectTraceRecorded carries a completed request trace.
	SubjectTraceRecorded = "cr.trace.recorded"
	// SubjectRequestLogged carries a request log entry for the debug view.
	SubjectRequestLogged = "cr.log.request"
	// SubjectProviderHealth carries a provider health assessment.
	SubjectProviderHealth = "cr.provider.health"
	// SubjectProviderStatusChange carries a provider status transition.
	SubjectProviderStatusChange = "cr.provider.status"
	// SubjectEvalJob carries an evaluation job request for the Python workers.
	SubjectEvalJob = "cr.eval.job"
	// SubjectEvalResult carries an evaluation result back to the control plane.
	SubjectEvalResult = "cr.eval.result"
	// SubjectReplayJob carries a replay job request.
	SubjectReplayJob = "cr.replay.job"
	// SubjectAuditEvent carries a control-plane audit event.
	SubjectAuditEvent = "cr.audit.event"
	// SubjectToolRunCompleted carries a finished bounded tool run: its status,
	// what ran, and why it stopped. Downstream consumers get the same summary
	// the client saw, so the dashboard and the event stream cannot disagree.
	SubjectToolRunCompleted = "cr.tool.run.completed"
	// SubjectCacheInvalidated carries a cache flush: scope, target, reason and
	// how many entries were removed. Published best-effort from the admin
	// path so cache behaviour is observable without polling.
	SubjectCacheInvalidated = "cr.cache.invalidated"
	// SubjectTunnelStatus carries a tunnel lifecycle transition: created,
	// url, restarted, stopped or failed, with the session summary. Published
	// best-effort so tunnel state is observable without polling the API.
	SubjectTunnelStatus = "cr.tunnel.status"
)

// SubjectWildcard is the prefix every CoreRouter subject shares. It is used to
// scope a NATS account or a stream to this application's traffic.
const SubjectWildcard = "cr.>"

// JetStream stream and consumer names.
const (
	// StreamUsage is the durable stream retaining usage and trace events.
	StreamUsage = "COREROUTER_USAGE"
	// StreamJobs is the durable stream retaining evaluation and replay jobs.
	StreamJobs = "COREROUTER_JOBS"
	// StreamEvents is the durable stream retaining audit and health events.
	StreamEvents = "COREROUTER_EVENTS"
)

// NATS is the asynchronous messaging layer.
//
// The synchronous request path never publishes: a request completes, and the
// resulting records are handed to an in-process buffer that publishes
// asynchronously. That keeps a NATS outage from adding latency to inference, which
// is the whole reason the async layer exists.
type NATS struct {
	conn   *nats.Conn
	js     nats.JetStreamContext
	cfg    config.NATSConfig
	logger *slog.Logger

	publishMu sync.Mutex
	published int64
	failed    int64
	dropped   int64
}

// NewNATS connects to NATS and, when enabled, provisions JetStream streams.
func NewNATS(ctx context.Context, cfg config.NATSConfig, logger *slog.Logger) (*NATS, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.URL == "" && len(cfg.URLs) == 0 {
		return nil, fmt.Errorf("nats url is empty")
	}

	options := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait.Std()),
		nats.Timeout(5 * time.Second),
		// Disconnect and reconnect handlers turn a silent messaging outage into an
		// observable log line, which is otherwise very hard to diagnose because the
		// client keeps accepting publishes into its buffer.
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logger.Info("nats connection closed")
		}),
	}
	if cfg.Token != "" {
		options = append(options, nats.Token(cfg.Token))
	}
	if cfg.CredentialsFile != "" {
		options = append(options, nats.UserCredentials(cfg.CredentialsFile))
	}

	url := cfg.URL
	if url == "" && len(cfg.URLs) > 0 {
		url = cfg.URLs[0]
	}

	conn, err := nats.Connect(url, options...)
	if err != nil {
		return nil, fmt.Errorf("connect to nats at %s: %w", url, err)
	}

	out := &NATS{conn: conn, cfg: cfg, logger: logger}

	if cfg.JetStream {
		js, err := conn.JetStream()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("create jetstream context: %w", err)
		}
		out.js = js

		if err := out.ensureStreams(cfg.StreamReplicas); err != nil {
			// A failure to provision streams is logged but not fatal: core NATS
			// publishing still works, and the workers can bind their own streams.
			logger.Warn("failed to provision jetstream streams", "error", err)
		}
	}

	logger.Info("connected to nats", "url", url, "jetstream", cfg.JetStream)
	return out, nil
}

// ensureStreams creates the streams CoreRouter owns, idempotently.
func (n *NATS) ensureStreams(replicas int) error {
	if replicas < 1 {
		replicas = 1
	}

	streams := []nats.StreamConfig{
		{
			Name:     StreamUsage,
			Subjects: []string{SubjectUsageRecorded, SubjectTraceRecorded, SubjectRequestLogged},
			// Usage and trace events are retained for a week. That window is long
			// enough for a consumer outage to be survivable and short enough that
			// the stream does not become a second, unmanaged copy of ClickHouse.
			MaxAge:    7 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  replicas,
			Retention: nats.LimitsPolicy,
			// DiscardOld rather than DiscardNew: under a consumer outage the newest
			// telemetry is more valuable than the oldest.
			Discard: nats.DiscardOld,
			MaxMsgs: 1_000_000,
		},
		{
			Name:      StreamJobs,
			Subjects:  []string{SubjectEvalJob, SubjectReplayJob},
			MaxAge:    30 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  replicas,
			Retention: nats.WorkQueuePolicy,
		},
		{
			Name:      StreamEvents,
			Subjects:  []string{SubjectProviderHealth, SubjectProviderStatusChange, SubjectAuditEvent, SubjectToolRunCompleted, SubjectCacheInvalidated, SubjectTunnelStatus},
			MaxAge:    30 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  replicas,
			Retention: nats.LimitsPolicy,
			Discard:   nats.DiscardOld,
			MaxMsgs:   500_000,
		},
	}

	for _, stream := range streams {
		if _, err := n.js.StreamInfo(stream.Name); err == nil {
			// The stream exists. Updating it on every startup would fight with any
			// deliberate operator change, so it is left alone.
			continue
		}
		if _, err := n.js.AddStream(&stream); err != nil {
			return fmt.Errorf("create stream %s: %w", stream.Name, err)
		}
		n.logger.Info("created jetstream stream", "stream", stream.Name)
	}
	return nil
}

// Conn exposes the underlying connection.
func (n *NATS) Conn() *nats.Conn { return n.conn }

// JetStream exposes the JetStream context, which may be nil.
func (n *NATS) JetStream() nats.JetStreamContext { return n.js }

// Close drains and closes the connection.
func (n *NATS) Close() error {
	if n == nil || n.conn == nil {
		return nil
	}
	// Drain flushes buffered publishes before closing, so telemetry already
	// accepted by the client is not lost on a graceful shutdown.
	if err := n.conn.Drain(); err != nil {
		n.conn.Close()
		return err
	}
	return nil
}

// Publish sends raw bytes on a subject.
//
// Delivery failures are counted rather than returned: every caller on the request
// path treats the async layer as best-effort, and propagating a publish error
// would invite a caller to fail a request that already succeeded.
func (n *NATS) Publish(subject string, payload []byte) {
	if n == nil || n.conn == nil {
		return
	}

	n.publishMu.Lock()
	defer n.publishMu.Unlock()

	if err := n.conn.Publish(subject, payload); err != nil {
		n.failed++
		n.logger.Warn("nats publish failed", "subject", subject, "error", err)
		return
	}
	n.published++
}

// PublishJSON marshals and publishes a value.
func (n *NATS) PublishJSON(subject string, value any) {
	if n == nil || n.conn == nil {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		n.dropped++
		n.logger.Warn("failed to encode a nats payload", "subject", subject, "error", err)
		return
	}
	n.Publish(subject, payload)
}

// Stats reports publish counters for the metrics endpoint.
func (n *NATS) Stats() map[string]any {
	if n == nil || n.conn == nil {
		return nil
	}
	n.publishMu.Lock()
	defer n.publishMu.Unlock()

	return map[string]any{
		"published":      n.published,
		"publish_failed": n.failed,
		"encode_dropped": n.dropped,
		"reconnects":     n.conn.Reconnects,
		"in_msgs":        n.conn.Stats().InMsgs,
		"out_msgs":       n.conn.Stats().OutMsgs,
		"connected":      n.conn.IsConnected(),
	}
}

// Subscribe registers a core NATS subscription with a queue group.
//
// A queue group is always used so that running two gateway replicas does not
// process every message twice. The handler runs on the NATS delivery goroutine, so
// implementations must not block for long; heavy work belongs in a JetStream
// consumer with explicit acknowledgement.
func (n *NATS) Subscribe(subject, queue string, handler func(msg *nats.Msg)) (*nats.Subscription, error) {
	if n == nil || n.conn == nil {
		return nil, fmt.Errorf("nats is not initialized")
	}
	if queue == "" {
		queue = "corerouter"
	}
	return n.conn.QueueSubscribe(subject, queue, handler)
}

// SubscribeJetStream registers a durable JetStream consumer.
//
// Durability is what makes the eval and replay workers restartable: a worker that
// dies mid-job resumes from its last acknowledgement instead of losing the job.
func (n *NATS) SubscribeJetStream(subject, durable string, handler func(msg *nats.Msg) error) (*nats.Subscription, error) {
	if n == nil || n.js == nil {
		return nil, fmt.Errorf("jetstream is not enabled")
	}

	return n.js.QueueSubscribe(subject, durable, func(msg *nats.Msg) {
		if err := handler(msg); err != nil {
			n.logger.Warn("jetstream handler failed",
				"subject", subject, "durable", durable, "error", err)
			// A negative acknowledgement asks for redelivery, which is the correct
			// response to a transient handler failure.
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	},
		nats.Durable(durable),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.DeliverAll(),
		nats.MaxDeliver(5),
	)
}

// PublishUsage publishes a usage record for downstream consumers.
func (n *NATS) PublishUsage(rec *domain.UsageRecord) {
	n.PublishJSON(SubjectUsageRecorded, rec)
}

// PublishTrace publishes a request trace for downstream consumers.
func (n *NATS) PublishTrace(trace *domain.RequestTrace) {
	n.PublishJSON(SubjectTraceRecorded, trace)
}

// PublishRequestLog publishes a request log entry.
func (n *NATS) PublishRequestLog(entry *domain.RequestLog) {
	n.PublishJSON(SubjectRequestLogged, entry)
}

// PublishProviderHealth publishes a provider health assessment.
func (n *NATS) PublishProviderHealth(health *domain.ProviderHealth) {
	n.PublishJSON(SubjectProviderHealth, health)
}

// PublishAudit publishes an audit event.
func (n *NATS) PublishAudit(event *domain.AuditEvent) {
	n.PublishJSON(SubjectAuditEvent, event)
}

// PublishToolRunCompleted publishes a finished bounded tool run.
func (n *NATS) PublishToolRunCompleted(run *domain.ToolRunEvent) {
	n.PublishJSON(SubjectToolRunCompleted, run)
}

// PublishCacheInvalidated publishes a cache flush event.
func (n *NATS) PublishCacheInvalidated(event *domain.CacheEvent) {
	n.PublishJSON(SubjectCacheInvalidated, event)
}

// PublishTunnelStatus publishes a tunnel lifecycle transition.
func (n *NATS) PublishTunnelStatus(event *domain.TunnelEvent) {
	n.PublishJSON(SubjectTunnelStatus, event)
}

// PublishEvalJob enqueues an evaluation job for the Python workers.
func (n *NATS) PublishEvalJob(job any) {
	n.PublishJSON(SubjectEvalJob, job)
}

// EnsureContext is a small helper for callers that need to bound a publish with a
// deadline, such as the admin API triggering a health check.
func EnsureContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
