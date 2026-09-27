package observability

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransportPropagatesWithoutExportingSecrets(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://coordinator.test/api/hosts/private-id?token=private-query", nil)
	req.Header.Set("Authorization", "Bearer private-token")
	transport := Transport{Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("traceparent") == "" {
			t.Fatal("missing trace context")
		}
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("authentication changed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("private body"))}, nil
	})}
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if req.Header.Get("traceparent") != "" {
		t.Fatal("modified caller request")
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans=%d", len(spans))
	}
	for _, attr := range spans[0].Attributes() {
		if strings.Contains(attr.Value.AsString(), "private") {
			t.Fatal("private request data exported")
		}
	}
}
func TestCollectorIsOptInAndRejectsUntrustedEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = shutdown(context.Background())
	for _, endpoint := range []string{"http://remote.example:4318", "https://user:password@collector.test", "https://collector.test?token=secret"} {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
		if _, err := Start(context.Background()); err == nil {
			t.Fatal("accepted invalid endpoint")
		}
	}
}
func TestOperationRedactsAllParameters(t *testing.T) {
	if Operation("/api/drive/private/financial.pdf") != "/api/drive" {
		t.Fatal("unexpected class")
	}
	if Operation("/api/hosts-private") != "other" {
		t.Fatal("prefix boundary missing")
	}
}

func TestRuntimeMetricsAreRealProcessReadings(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	if err := registerRuntimeMetrics(provider.Meter("test")); err != nil {
		t.Fatal(err)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			found[m.Name] = true
			switch value := m.Data.(type) {
			case metricdata.Gauge[int64]:
				if len(value.DataPoints) != 1 || value.DataPoints[0].Value <= 0 {
					t.Fatalf("invalid %s", m.Name)
				}
			case metricdata.Sum[float64]:
				if len(value.DataPoints) != 1 || value.DataPoints[0].Value < 0 || !value.IsMonotonic {
					t.Fatalf("invalid %s", m.Name)
				}
			default:
				t.Fatalf("unexpected metric type %s", m.Name)
			}
		}
	}
	for _, name := range []string{"nexal.connector.runtime.heap.bytes", "nexal.connector.runtime.goroutines", "nexal.connector.runtime.gc.cpu.seconds"} {
		if !found[name] {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestCapacityAndThreatScanLifecycles(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	m := provider.Meter("lifecycle-test")
	oldCapacity, oldActive, oldDuration, oldCompleted := capacity, active, scanDuration, scansCompleted
	defer func() {
		capacity, active, scanDuration, scansCompleted = oldCapacity, oldActive, oldDuration, oldCompleted
	}()
	capacity, _ = m.Int64UpDownCounter("capacity")
	active, _ = m.Int64UpDownCounter("active")
	scanDuration, _ = m.Float64Histogram("duration")
	scansCompleted, _ = m.Int64Counter("completed")
	ctx := context.Background()
	release := ExecutorCapacity(ctx)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	initial := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, observed := range scope.Metrics {
			initial[observed.Name] = observed.Data.(metricdata.Sum[int64]).DataPoints[0].Value
		}
	}
	if got, ok := initial["active"]; !ok || got != 0 {
		t.Fatalf("idle active not observed: %v", initial)
	}
	if got := initial["capacity"]; got != 1 {
		t.Fatalf("capacity=%d", got)
	}
	active.Add(ctx, 1)
	secondRelease := ExecutorCapacity(ctx)
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, observed := range scope.Metrics {
			if observed.Name == "active" && observed.Data.(metricdata.Sum[int64]).DataPoints[0].Value != 1 {
				t.Fatal("second executor reset active tasks")
			}
		}
	}
	secondRelease()
	active.Add(ctx, -1)
	release()
	release() // cleanup must not undercount capacity
	finish := ThreatScan(ctx)
	finish("engine_failed")
	finish("completed") // deferred cleanup must not create a second scan
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch metric.Name {
			case "capacity":
				if metric.Data.(metricdata.Sum[int64]).DataPoints[0].Value != 0 {
					t.Fatal("capacity leaked")
				}
			case "duration":
				points := metric.Data.(metricdata.Histogram[float64]).DataPoints
				if len(points) != 1 || points[0].Count != 1 || points[0].Sum < 0 {
					t.Fatal("scan counted more than once")
				}
			case "completed":
				points := metric.Data.(metricdata.Sum[int64]).DataPoints
				if len(points) != 1 || points[0].Value != 1 {
					t.Fatal("invalid outcomes")
				}
				value, ok := points[0].Attributes.Value("outcome")
				if !ok || value.AsString() != "engine_failed" {
					t.Fatal("lost initial failure outcome")
				}
			}
		}
	}
}
