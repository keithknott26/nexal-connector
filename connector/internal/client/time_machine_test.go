package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nexal/connector/internal/timemachine"
)

func TestTimeMachineCoordinatorContract(t *testing.T) {
	var reported timemachine.Status
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-token-123456" {
			t.Error("missing host auth")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v2/devices/host1/time-machine/config":
			_ = json.NewEncoder(w).Encode(timemachine.Config{Enabled: true, Entitled: true, Revision: 2, ShareName: "NexalBackup", MountPath: "/mnt/nexal-backup", Backend: timemachine.BackendJuiceFS, QuotaBytes: 100 << 30, MeshCIDRs: []string{"100.64.0.0/10"}, Advertise: true})
		case "POST /api/v2/devices/host1/time-machine/status":
			if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, "host-token-123456", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := c.TimeMachineConfig(context.Background(), "host1")
	if err != nil || cfg.Revision != 2 {
		t.Fatalf("%+v %v", cfg, err)
	}
	want := timemachine.Evaluate(cfg, timemachine.Observation{Platform: "linux", BackendMounted: true, BackendType: timemachine.BackendJuiceFS, FreeBytes: 200 << 30, SMBConfigured: true, SMBHealthy: true, Advertised: true, AdminAvailable: true})
	if err := c.ReportTimeMachineStatus(context.Background(), "host1", want); err != nil {
		t.Fatal(err)
	}
	if reported.State != "ready" || reported.Revision != 2 {
		t.Fatalf("%+v", reported)
	}
}
