package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"google.golang.org/grpc/credentials"

	"github.com/tbourn/go-chat-backend/internal/config"
)

// ---- TEST SEAMS (signatures exactly match what tests will assign) ----
var (
	newOTLPClient = otlptracegrpc.NewClient

	// No exporter options exposed here -> stable for tests.
	newOTLPExporterFn = func(ctx context.Context, client otlptrace.Client) (*otlptrace.Exporter, error) {
		return otlptrace.New(ctx, client)
	}

	newServiceResourceFn = func(ctx context.Context, serviceName, version string) (*resource.Resource, error) {
		return resource.New(
			ctx,
			resource.WithAttributes(
				semconv.ServiceName(serviceName),
				semconv.ServiceVersion(version),
			),
		)
	}
)

// ---------------------------------------------------------------------

// SetupOTel configures OpenTelemetry tracing and returns a shutdown function.
func SetupOTel(ctx context.Context, cfg config.OTELConfig, version string) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	// Build OTLP gRPC client options
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	} else {
		creds := credentials.NewClientTLSFromCert(nil, "")
		opts = append(opts, otlptracegrpc.WithTLSCredentials(creds))
	}

	// Exporter via seam
	client := newOTLPClient(opts...)
	exp, err := newOTLPExporterFn(ctx, client)
	if err != nil {
		return nil, err
	}

	// Resource via seam
	res, err := newServiceResourceFn(ctx, cfg.ServiceName, version)
	if err != nil {
		return nil, err
	}

	// Tracer provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithResource(res),
	)

	// Globals
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// Shutdown
	return tp.Shutdown, nil
}
