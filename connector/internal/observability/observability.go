// Package observability exports bounded, content-free operational telemetry.
package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	runtimemetrics "runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var meter = otel.Meter("nexal.connector")
var requests, _ = meter.Int64Counter("nexal.http.client.requests")
var duration, _ = meter.Float64Histogram("nexal.http.client.duration", metric.WithUnit("s"))
var payload, _ = meter.Int64Counter("nexal.http.client.payload.bytes")
var active, _ = meter.Int64UpDownCounter("nexal.compute.active")
var capacity, _ = meter.Int64UpDownCounter("nexal.compute.capacity")
var scanDuration, _ = meter.Float64Histogram("nexal.threat.scan.duration", metric.WithUnit("s"))
var scansCompleted, _ = meter.Int64Counter("nexal.threat.scan.completed")
var started, _ = meter.Int64Counter("nexal.compute.started")
var completed, _ = meter.Int64Counter("nexal.compute.completed")
var workDuration, _ = meter.Float64Histogram("nexal.compute.duration", metric.WithUnit("s"))
var peerBytes, _ = meter.Int64Counter("nexal.peer.transfer.bytes")

// Start is opt-in. Ordinary installations need no exporter, listener or shared key.
// Configure only a trusted local/managed collector; never an endpoint from pairing.
func Start(ctx context.Context) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, errors.New("invalid telemetry collector endpoint")
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("telemetry redirect denied") }}
	traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/traces"), otlptracehttp.WithHTTPClient(client), otlptracehttp.WithTimeout(3*time.Second))
	if err != nil {
		return nil, errors.New("telemetry trace exporter unavailable")
	}
	metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/metrics"), otlpmetrichttp.WithHTTPClient(client), otlpmetrichttp.WithTimeout(3*time.Second))
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, errors.New("telemetry metric exporter unavailable")
	}
	res := resource.NewWithAttributes("", attribute.String("service.name", "nexal-connector"), attribute.String("service.instance.id", uuid.NewString()))
	traces := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.05))), sdktrace.WithBatcher(traceExporter, sdktrace.WithMaxQueueSize(512), sdktrace.WithExportTimeout(3*time.Second)))
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(30*time.Second), sdkmetric.WithTimeout(3*time.Second))))
	if err := registerRuntimeMetrics(metrics.Meter("nexal.connector.runtime")); err != nil {
		_ = metrics.Shutdown(ctx)
		_ = traces.Shutdown(ctx)
		return nil, errors.New("runtime telemetry unavailable")
	}
	otel.SetTracerProvider(traces)
	otel.SetMeterProvider(metrics)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	// Exporter errors can contain endpoint/header details. Do not print raw errors.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	return func(ctx context.Context) error { return errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx)) }, nil
}

// Operation intentionally captures only an allowlisted API family, never a path parameter.
func Operation(path string) string {
	for _, family := range []string{"/api/hosts", "/api/attempts", "/api/drive", "/api/v2", "/api/home", "/api/mcp", "/api/enrollments", "/api/transfers"} {
		if path == family || strings.HasPrefix(path, family+"/") {
			return family
		}
	}
	return "other"
}

type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	operation := Operation(req.URL.Path)
	method := req.Method
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		method = "OTHER"
	}
	attrs := []attribute.KeyValue{attribute.String("http.request.method", method), attribute.String("nexal.operation", operation)}
	ctx, span := otel.Tracer("nexal.connector").Start(req.Context(), "coordinator "+operation, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	copy := req.Clone(ctx)
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(copy.Header))
	if copy.Body != nil {
		copy.Body = &countedBody{ReadCloser: copy.Body, count: func(n int64) { payload.Add(ctx, n, metric.WithAttributes(attribute.String("direction", "sent"))) }}
	}
	start := time.Now()
	response, err := t.Base.RoundTrip(copy)
	status := 0
	if response != nil {
		status = response.StatusCode
		if response.Body != nil {
			response.Body = &countedBody{ReadCloser: response.Body, count: func(n int64) { payload.Add(ctx, n, metric.WithAttributes(attribute.String("direction", "received"))) }}
		}
	}
	attrs = append(attrs, attribute.Int("http.response.status_code", status))
	span.SetAttributes(attrs...)
	if err != nil || status >= 500 {
		span.SetStatus(codes.Error, "request failed")
	}
	requests.Add(ctx, 1, metric.WithAttributes(attrs...))
	duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	return response, err
}

type countedBody struct {
	io.ReadCloser
	count func(int64)
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.count(int64(n))
	}
	return n, err
}

// Compute starts only after admission and durable replay protection succeed.
func Compute(ctx context.Context) func(bool) {
	started.Add(ctx, 1)
	active.Add(ctx, 1)
	start := time.Now()
	return func(success bool) {
		active.Add(ctx, -1)
		attrs := metric.WithAttributes(attribute.Bool("success", success))
		completed.Add(ctx, 1, attrs)
		workDuration.Record(ctx, time.Since(start).Seconds(), attrs)
	}
}
func PeerTransfer(ctx context.Context, direction string, n int64) {
	if n > 0 && (direction == "sent" || direction == "received") {
		peerBytes.Add(ctx, n, metric.WithAttributes(attribute.String("direction", direction)))
	}
}

type PeerReader struct {
	io.Reader
	Context context.Context
}

func (r PeerReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	PeerTransfer(r.Context, "received", int64(n))
	return n, err
}

// Underlying preserves access to the validated TLS transport for WebSocket and long-request clones.
func Underlying(t http.RoundTripper) http.RoundTripper {
	if wrapped, ok := t.(Transport); ok {
		return wrapped.Base
	}
	return t
}

// Runtime readings measure the connector process, not the entire host or a worker count.
// Read is performed on the exporter cadence only; no background polling goroutine.
func registerRuntimeMetrics(m metric.Meter) error {
	heap, err := m.Int64ObservableGauge("nexal.connector.runtime.heap.bytes")
	if err != nil {
		return err
	}
	goroutines, err := m.Int64ObservableGauge("nexal.connector.runtime.goroutines")
	if err != nil {
		return err
	}
	gcCPU, err := m.Float64ObservableCounter("nexal.connector.runtime.gc.cpu.seconds")
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		samples := []runtimemetrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/goroutines:goroutines"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
		runtimemetrics.Read(samples)
		if samples[0].Value.Kind() == runtimemetrics.KindUint64 {
			observer.ObserveInt64(heap, int64(samples[0].Value.Uint64()))
		}
		if samples[1].Value.Kind() == runtimemetrics.KindUint64 {
			observer.ObserveInt64(goroutines, int64(samples[1].Value.Uint64()))
		}
		if samples[2].Value.Kind() == runtimemetrics.KindFloat64 {
			observer.ObserveFloat64(gcCPU, samples[2].Value.Float64())
		}
		return nil
	}, heap, goroutines, gcCPU)
	return err
}

// ExecutorCapacity records the enforced one-at-a-time execution slot while an
// agent loop is running. It describes configured capacity, not admission readiness.
func ExecutorCapacity(ctx context.Context) func() {
	// Running the executor establishes an observed idle baseline before its first task.
	// Adding zero preserves any concurrent executor activity.
	active.Add(ctx, 0)
	capacity.Add(ctx, 1)
	var once sync.Once
	return func() { once.Do(func() { capacity.Add(ctx, -1) }) }
}

// ThreatScan covers actual engine preparation and scanning, excluding delivery.
// A once guard permits both deferred failure coverage and ending before delivery.
func ThreatScan(ctx context.Context) func(string) {
	started := time.Now()
	var once sync.Once
	return func(outcome string) {
		once.Do(func() {
			switch outcome {
			case "completed", "limited", "engine_unavailable", "engine_failed", "state_error":
			default:
				outcome = "state_error"
			}
			attrs := metric.WithAttributes(attribute.String("outcome", outcome))
			scanDuration.Record(ctx, time.Since(started).Seconds(), attrs)
			scansCompleted.Add(ctx, 1, attrs)
		})
	}
}
