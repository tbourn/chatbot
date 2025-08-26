package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/tbourn/go-chat-backend/internal/config"
)

func preserveOTelGlobals(t *testing.T) func() {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	return func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	}
}

func TestSetupOTel_Disabled_NoOp(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	shutdown, err := SetupOTel(context.Background(), config.OTELConfig{
		Enabled:     false,
		Insecure:    true,
		Endpoint:    "ignored:4317",
		ServiceName: "svc",
		SampleRatio: 1.0,
	}, "v0.0.0")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if shutdown == nil {
		t.Fatalf("expected non-nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned error: %v", err)
	}
}

func TestSetupOTel_Insecure_SetsProviderAndPropagator(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	cfg := config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc-insecure",
		SampleRatio: 1.0,
	}
	shutdown, err := SetupOTel(context.Background(), cfg, "v1.2.3")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("expected *sdktrace.TracerProvider")
	}

	// Exercise propagator: inject/extract
	prop := otel.GetTextMapPropagator()
	carrier := propagation.MapCarrier{}
	ctx2, span := otel.Tracer("test").Start(context.Background(), "span")
	span.End()
	prop.Inject(ctx2, carrier)
	_ = prop.Extract(context.Background(), carrier)
}

func TestSetupOTel_SecureTLS_SetsProvider(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	cfg := config.OTELConfig{
		Enabled:     true,
		Insecure:    false, // TLS branch
		Endpoint:    "localhost:4317",
		ServiceName: "svc-tls",
		SampleRatio: 1.0,
	}
	shutdown, err := SetupOTel(context.Background(), cfg, "v9.9.9")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("expected *sdktrace.TracerProvider")
	}

	tr := otel.Tracer("secure-test")
	_, span := tr.Start(context.Background(), "child")
	span.End()
}

func TestSetupOTel_CanceledContext_StillSucceeds(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // exporter creation should still succeed (lazy init)

	shutdown, err := SetupOTel(ctx, config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc-canceled",
		SampleRatio: 1.0,
	}, "vX.Y.Z")
	if err != nil {
		t.Fatalf("unexpected err with canceled ctx: %v", err)
	}
	if shutdown == nil {
		t.Fatalf("expected non-nil shutdown func")
	}
	_ = shutdown(context.Background())
}

func TestSetupOTel_ExporterError_Propagates_AndGlobalsIntact(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	orig := newOTLPExporterFn
	defer func() { newOTLPExporterFn = orig }()

	// **Signature matches exactly**
	newOTLPExporterFn = func(ctx context.Context, client otlptrace.Client) (*otlptrace.Exporter, error) {
		return nil, errors.New("boom-exporter")
	}

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()

	_, err := SetupOTel(context.Background(), config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc",
		SampleRatio: 1.0,
	}, "v0")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if otel.GetTracerProvider() != prevTP {
		t.Fatalf("tracer provider changed on failure")
	}
	if otel.GetTextMapPropagator() != prevProp {
		t.Fatalf("propagator changed on failure")
	}
}

func TestSetupOTel_ResourceError_Propagates_AndGlobalsIntact(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	orig := newServiceResourceFn
	defer func() { newServiceResourceFn = orig }()

	// **Signature matches exactly**
	newServiceResourceFn = func(ctx context.Context, serviceName, version string) (*resource.Resource, error) {
		return nil, errors.New("boom-resource")
	}

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()

	_, err := SetupOTel(context.Background(), config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc",
		SampleRatio: 1.0,
	}, "v0")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if otel.GetTracerProvider() != prevTP {
		t.Fatalf("tracer provider changed on failure")
	}
	if otel.GetTextMapPropagator() != prevProp {
		t.Fatalf("propagator changed on failure")
	}
}

func TestShutdown_IsCallable(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	shutdown, err := SetupOTel(context.Background(), config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc-shutdown",
		SampleRatio: 1.0,
	}, "v1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	ct, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := shutdown(ct); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestSpanCreation_Smoke(t *testing.T) {
	restore := preserveOTelGlobals(t)
	defer restore()

	shutdown, err := SetupOTel(context.Background(), config.OTELConfig{
		Enabled:     true,
		Insecure:    true,
		Endpoint:    "localhost:4317",
		ServiceName: "svc-span",
		SampleRatio: 1.0,
	}, "v1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	tr := otel.Tracer("smoke")
	_, span := tr.Start(context.Background(), "root", trace.WithSpanKind(trace.SpanKindInternal))
	span.End()
}
