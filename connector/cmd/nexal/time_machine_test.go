package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nexal/connector/internal/config"
)

func TestTimeMachineCommandFailsClosedBeforeCredentialMint(t *testing.T) {
	credentialCalls := 0
	statusState := ""
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/devices/host1/time-machine/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "serviceState": "provisioning", "computerId": "computer1", "networkId": "network1", "protocol": "smb", "port": 445, "bonjour": map[string]any{"serviceType": "_adisk._tcp", "shareName": "NexalBackup"}, "storage": map[string]any{"driver": "juicefs", "backend": "r2", "objectPrefix": "tenants/t/time-machine/network1/", "credentialEndpoint": "/api/v2/devices/host1/time-machine/credentials", "credentialsIncluded": false}, "quotaBytes": uint64(100 << 30), "refreshAfterSeconds": 60})
		case "/api/v2/devices/host1/time-machine/credentials":
			credentialCalls++
		case "/api/v2/devices/host1/time-machine/status":
			var b struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			statusState = b.State
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "config.json")
	c := config.Config{Version: 1, Coordinator: s.URL, Name: "Mac", HostID: "host1", Listen: "127.0.0.1:8788", Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.NewSecrets(p, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = secrets.Put(context.Background(), "host", "host-token-123456789012345678901234"); err != nil {
		t.Fatal(err)
	}
	if err := timeMachineCommand(context.Background(), []string{"--config", p}); err != nil {
		t.Fatal(err)
	}
	if credentialCalls != 0 {
		t.Fatal("minted an unusable storage credential")
	}
	if statusState != "error" {
		t.Fatalf("status=%q", statusState)
	}
}
