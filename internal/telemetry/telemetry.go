// Package telemetry wires up observability: structured logs (log/slog)
// and OpenTelemetry traces and metrics.
//
// OpenTelemetry (OTel) is the vendor-neutral standard for telemetry. The
// service exports data with OTLP (the OTel protocol) to any compatible
// backend: an OTel Collector, Grafana, Datadog, Honeycomb, ... Switching
// vendors is a configuration change, not a code change.
package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Config describes the service for telemetry purposes.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	// Enabled turns exporting on. When false, OTel keeps its default
	// no-op providers: instrumentation calls cost almost nothing and
	// nothing is sent anywhere.
	Enabled bool
}

// ShutdownFunc flushes buffered telemetry and releases resources.
type ShutdownFunc func(context.Context) error

// Setup installs the global OpenTelemetry trace and meter providers.
//
// Exporter settings (endpoint, headers, protocol, sampling...) are read
// by the SDK from the standard OTEL_* environment variables, e.g.
// OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318.
//
// The returned ShutdownFunc must be called before the process exits;
// otherwise the last batch of spans and metrics is lost.
func Setup(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	// Propagators read and write the W3C "traceparent" header, so a trace
	// started by a caller continues through this service (distributed
	// tracing). Installed even when export is disabled.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	// The resource describes "who" produced the telemetry; every span and
	// metric carries these attributes.
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentName(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}

	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	// The batcher buffers spans and sends them in the background, so
	// requests never wait on the telemetry backend.
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create metric exporter: %w", err),
			tracerProvider.Shutdown(ctx),
		)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	shutdown := func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}

	return shutdown, nil
}
