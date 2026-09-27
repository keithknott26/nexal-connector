# Connector OpenTelemetry

Optional export uses the official OpenTelemetry Go SDK. Without `OTEL_EXPORTER_OTLP_ENDPOINT`, instruments are no-ops. Set a trusted HTTPS collector base URL (HTTP is allowed only on loopback) and standard `OTEL_EXPORTER_OTLP_HEADERS` for its provisioned credential. Do not embed a shared fleet ingestion key in distributed application binaries.

The SDK exports cumulative metrics every 30 seconds and samples root traces at 5%. Span queues are capped at 512; exporter I/O has a separate three-second timeout; shutdown flush is capped at four seconds. Each process has a random service instance ID to avoid merging cumulative counters across processes.

Instrumented boundaries: coordinator HTTP transport, admitted compute execution, and authenticated peer object-server payload streams. HTTP attributes use an allowlisted API family, method and status; request URLs, queries, headers, identities and content are never exported. Payload counters measure actual reader consumption (or peer response writes), not Content-Length claims. Compute retries of result submission do not increment starts. Active compute returns to zero on every exit path.

The transport decorator preserves the original TLS/redirect/proxy policy. WebSocket TLS cloning and long exit-route transport cloning unwrap the decorator and retain the underlying policy. Existing peer authentication and storage limits remain authoritative.

Dashboard consumers and Collector/Prometheus deployment are defined in nexal-platform/docs/observability/OPERATIONS.md. Existing tunnel reports remain the source for traffic per reporting peer; payload counters here intentionally do not attach customer object keys or unbounded peer labels.
