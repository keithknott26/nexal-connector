package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"nexal/connector/internal/remoteinference"
)

// The offload and stats endpoints are lenient: unknown server fields must not
// break an installed connector, and ids are validated before anything is sent.
func TestRemoteOffloadAndStatsRoutes(t *testing.T) {
	var got []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"hosts":[{"hostId":"h2","name":"Studio","reportedAt":"now","stats":{"localRequests":3},"futureField":1}],"extra":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"futureField":1}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	hosts, err := c.InferenceStats(ctx, "h1")
	if err != nil || len(hosts) != 1 || hosts[0].HostID != "h2" || hosts[0].Name != "Studio" || hosts[0].Stats.LocalRequests != 3 {
		t.Fatalf("stats: %+v %v", hosts, err)
	}

	if err := c.SetRemoteOffload(ctx, "h1", "t1", remoteinference.OffloadPolicy{Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRemoteOffload(ctx, "h1", "t1", remoteinference.OffloadPolicy{Mode: "gpu", MaxPromptTokens: 512, MaxWaitSeconds: 9, GPU: &remoteinference.SharedGPU{}}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportRemoteOffload(ctx, "h1", "t1", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportRemoteOffload(ctx, "h1", "t1", "ok", "pk"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /api/v2/hosts/h1/inference-stats",
		"POST /api/v2/hosts/h1/remote-inference/t1/offload",
		"POST /api/v2/hosts/h1/remote-inference/t1/offload",
		"POST /api/v2/hosts/h1/remote-inference/t1/offload-state",
		"POST /api/v2/hosts/h1/remote-inference/t1/offload-state",
	}
	if len(got) != len(want) {
		t.Fatalf("requests: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
	// Zero-valued optional policy fields are omitted, not sent as zero.
	if len(bodies[1]) != 1 || bodies[1]["mode"] != "auto" {
		t.Fatalf("minimal offload body: %v", bodies[1])
	}
	for _, k := range []string{"mode", "maxPromptTokens", "maxWaitSeconds", "gpu"} {
		if _, ok := bodies[2][k]; !ok {
			t.Fatalf("full offload body missing %s: %v", k, bodies[2])
		}
	}
	if _, ok := bodies[3]["publicKey"]; ok {
		t.Fatalf("empty public key must be omitted: %v", bodies[3])
	}
	if bodies[4]["publicKey"] != "pk" || bodies[4]["note"] != "ok" {
		t.Fatalf("offload-state body: %v", bodies[4])
	}

	before := len(got)
	if _, err := c.InferenceStats(ctx, "bad/host"); err == nil {
		t.Fatal("bad host id accepted")
	}
	if err := c.SetRemoteOffload(ctx, "h1", "../x", remoteinference.OffloadPolicy{Mode: "auto"}); err == nil {
		t.Fatal("bad task id accepted")
	}
	if err := c.ReportRemoteOffload(ctx, "bad/host", "t1", "n", ""); err == nil {
		t.Fatal("bad host id accepted")
	}
	if len(got) != before {
		t.Fatalf("invalid ids reached the network: %v", got[before:])
	}
}
