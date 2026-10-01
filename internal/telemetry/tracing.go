package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/shadowsafin/corerouter/internal/config"
)

// TracerName is the instrumentation scope name for spans CoreRouter creates.
const TracerName = "github.com/shadowsafin/corerouter"

// Tracer bundles the tracing and metering providers so a single Shutdown releases
// both. The OTel SDK requires the same treatment for each signal, and a
// shutdown that misses one leaks an exporter goroutine per process.
type Tracer struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *metric.MeterProvider
	tracer         trace.Tracer
	logger         *slog.Logger
	enabled        bool
}

// NewTracer builds the OTel providers.
//
// When tracing is disabled or no endpoint is configured, a no-op tracer is
// installed rather than nothing at all. Instrumentation code can then call
// Tracer.Start unconditionally, which is what keeps span creation from becoming a
// constant source of nil checks and forgotten guards.
func NewTracer(ctx context.Context, cfg config.TelemetryConfig, app config.AppConfig, logger *slog.Logger) (*Tracer, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if !cfg.TracesEnabled && !cfg.MetricsEnabled {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return &Tracer{tracer: noop.NewTracerProvider().Tracer(TracerName), logger: logger}, nil
	}
	if cfg.OTLPEndpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		logger.Info("OpenTelemetry endpoint is not configured; traces are disabled")
		return &Tracer{tracer: noop.NewTracerProvider().Tracer(TracerName), logger: logger}, nil
	}

	// The resource identifies this process in every exported signal. Service
	// instance id is what lets an operator tell two replicas apart in a trace view.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceInstanceID(app.InstanceID),
			// The deployment environment attribute was renamed across semconv
			// versions, so the stable attribute key is set explicitly rather than
			// depending on a helper that moves between minor releases.
			attribute.String("deployment.environment.name", app.Environment),
			attribute.String("corerouter.region", app.Region),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		// Resource detection failures are not fatal: a trace without host
		// attributes still answers the question the operator asked.
		logger.Warn("failed to build the OpenTelemetry resource", "error", err)
		res = resource.NewSchemaless(semconv.ServiceName(cfg.ServiceName))
	}

	tracerProvider, err := buildTracerProvider(ctx, cfg, res, logger)
	if err != nil {
		return nil, err
	}

	meterProvider, err := buildMeterProvider(ctx, cfg, res, logger)
	if err != nil {
		// Shut the tracer provider down before returning, so a partial failure
		// does not leak the exporter it already started.
		if tracerProvider != nil {
			_ = tracerProvider.Shutdown(ctx)
		}
		return nil, err
	}

	otel.SetTracerProvider(tracerProvider)
	if meterProvider != nil {
		otel.SetMeterProvider(meterProvider)
	}

	// W3C trace context is the default because it is what most proxies and
	// collectors assume; baggage is included so vendor-neutral metadata survives a
	// multi-hop call.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logger.Info("OpenTelemetry configured",
		"endpoint", cfg.OTLPEndpoint,
		"traces", cfg.TracesEnabled,
		"metrics", cfg.MetricsEnabled,
		"sample_ratio", cfg.TraceSampleRatio,
	)

	return &Tracer{
		tracerProvider: tracerProvider,
		meterProvider:  meterProvider,
		tracer:         tracerProvider.Tracer(TracerName),
		logger:         logger,
		enabled:        true,
	}, nil
}

// buildTracerProvider constructs the trace provider and its OTLP exporter.
func buildTracerProvider(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	if !cfg.TracesEnabled {
		return nil, nil
	}

	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.OTLPEndpoint)}
	if cfg.OTLPInsecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if len(cfg.OTLPHeaders) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.OTLPHeaders))
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create the OTLP trace exporter: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio))
	if cfg.TraceSampleRatio >= 1 {
		sampler = sdktrace.ParentBased(sdktrace.AlwaysSample())
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			// A short batch delay keeps traces near-real-time, which matters when
			// an operator is watching a trace to debug a live issue.
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	), nil
}

// buildMeterProvider constructs the metric provider and its OTLP exporter.
func buildMeterProvider(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource, logger *slog.Logger) (*metric.MeterProvider, error) {
	if !cfg.MetricsEnabled {
		return nil, nil
	}

	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint)}
	if cfg.OTLPInsecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if len(cfg.OTLPHeaders) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(cfg.OTLPHeaders))
	}

	exporter, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create the OTLP metric exporter: %w", err)
	}

	interval := cfg.ExportInterval.Std()
	if interval <= 0 {
		interval = 15 * time.Second
	}

	provider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(interval))),
	)
	logger.Debug("OTLP metric export enabled", "interval", interval)
	return provider, nil
}

// Tracer returns the instrumentation tracer.
func (t *Tracer) Tracer() trace.Tracer {
	if t == nil || t.tracer == nil {
		return noop.NewTracerProvider().Tracer(TracerName)
	}
	return t.tracer
}

// Enabled reports whether spans are actually being recorded.
func (t *Tracer) Enabled() bool { return t != nil && t.enabled }

// Shutdown flushes and stops the providers.
//
// The shutdown context is deliberately independent of the request context that
// triggered it: on SIGTERM the request context is already cancelled, and reusing
// it would abort the final export and lose the last batch of traces.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}

	var firstErr error
	if t.tracerProvider != nil {
		if err := t.tracerProvider.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if t.meterProvider != nil {
		if err := t.meterProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Start begins a span, returning the context and the span.
//
// It is a method rather than a free function so a nil Tracer is safe: the no-op
// tracer is used, which means instrumentation never needs a nil check.
func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.Tracer().Start(ctx, name, opts...)
}
