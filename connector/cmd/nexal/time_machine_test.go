package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestTimeMachineGatewayClientConnect(t *testing.T) {
	const share = "tm33333333333343338333"
	dest := map[string]any{"protocol": "smb", "host": "tm-gw-1.netbird.cloud", "port": 445, "share": share, "username": share}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/devices/host1/time-machine/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "role": "client", "serviceState": "ready", "computerId": "c1", "networkId": "n1",
				"destination": dest, "credentialEndpoint": "/api/v2/devices/host1/time-machine/credentials", "credentialsIncluded": false, "quotaBytes": 1 << 40, "refreshAfterSeconds": 60})
		case "/api/v2/devices/host1/time-machine/credentials":
			body := map[string]any{"password": "abcdefghijklmnopqrstuvwx-_12", "url": "ignored"}
			for k, v := range dest {
				body[k] = v
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	p := writeTimeMachineTestConfig(t, s.URL)
	var got string
	oldSet, oldLookup := setDestination, meshLookup
	setDestination = func(_ context.Context, u string) error { got = u; return nil }
	meshLookup = func(string) ([]string, error) { return []string{"100.101.2.3"}, nil }
	defer func() { setDestination, meshLookup = oldSet, oldLookup }()
	if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect", "-dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatal("dry run called tmutil")
	}
	if runtime.GOOS == "darwin" {
		if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"}); err != nil {
			t.Fatal(err)
		}
		if got != "smb://"+share+":abcdefghijklmnopqrstuvwx-_12@tm-gw-1.netbird.cloud/"+share {
			t.Fatal(got)
		}
	}
	meshLookup = func(string) ([]string, error) { return []string{"51.81.1.1"}, nil }
	got = ""
	if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"}); err != nil || got != "" {
		t.Fatalf("public resolution must block without calling tmutil: %v %q", err, got)
	}
}

func writeTimeMachineTestConfig(t *testing.T, coordinator string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "config.json")
	c := config.Config{Version: 1, Coordinator: coordinator, Name: "Mac", HostID: "host1", Listen: "127.0.0.1:8788", Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
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
	return p
}
