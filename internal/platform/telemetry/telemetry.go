// Package telemetry sets up tracing.
//
// Tracing here is proportionate: spans cover the four places where this system
// crosses a boundary, which are the HTTP handler, the business transaction, the
// publish and the consume. Instrumenting every function would produce traces
// nobody reads and a bill nobody wants.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
)

// shutdownTimeout bounds the flush of pending spans at exit. A trace that is
// still being exported must not hold a deployment open.
const shutdownTimeout = 5 * time.Second

// Setup configures the global tracer provider and returns a shutdown function.
//
// The development exporter writes to stdout on purpose: a four container
// observability stack would cost more to run and read than the traces are worth
// at this size, and pointing the same code at a collector is one variable.
func Setup(ctx context.Context, cfg config.Config) (func(context.Context) error, error) {
	if !cfg.Telemetry.TracingEnabled || cfg.Telemetry.Exporter == "none" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// The schema url has to match the one resource.Default() carries, or the
	// merge fails at startup with a conflicting schema error. Pinning it to the
	// semconv package this build imports keeps the two in step.
	attrs, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		attribute.String("deployment.environment", string(cfg.Env)),
	))
	if err != nil {
		return nil, fmt.Errorf("build the telemetry resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(attrs),
		// The parent decision wins, so a sampled caller keeps its whole trace.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Telemetry.SampleRatio))),
	)

	otel.SetTracerProvider(provider)
	// W3C trace context is what the HTTP headers and the broker headers carry, so
	// a trace survives both hops.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	return func(shutdownCtx context.Context) error {
		bounded, cancel := context.WithTimeout(shutdownCtx, shutdownTimeout)
		defer cancel()
		if err := provider.Shutdown(bounded); err != nil {
			return fmt.Errorf("flush traces: %w", err)
		}
		return nil
	}, nil
}

func newExporter(ctx context.Context, cfg config.Config) (sdktrace.SpanExporter, error) {
	switch cfg.Telemetry.Exporter {
	case "otlp":
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.Telemetry.OTLPEndpoint))
		if err != nil {
			return nil, fmt.Errorf("create the otlp exporter: %w", err)
		}
		return exporter, nil
	default:
		exporter, err := stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
		if err != nil {
			return nil, fmt.Errorf("create the stdout exporter: %w", err)
		}
		return exporter, nil
	}
}

// Tracer returns the named tracer for a component.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// TraceIDFrom returns the trace identifier of the span in the context, or an
// empty string. Logging uses it so a log line and a span can be joined.
func TraceIDFrom(ctx context.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.HasTraceID() {
		return ""
	}
	return spanCtx.TraceID().String()
}
