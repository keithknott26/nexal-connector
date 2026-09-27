package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nexal/connector/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGuestRedeemPersistsLeaseBeforeAnyActivation(t *testing.T) {
	const session = "123e4567-e89b-12d3-a456-426614174000"
	const grant = "123e4567-e89b-12d3-a456-426614174001"
	const poll = "pppppppppppppppppppppppppppppppp"
	now := time.Now().UTC()
	calls := []string{}
	var deviceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/v2/enrollment-sessions":
			var requested map[string]any
			json.NewDecoder(r.Body).Decode(&requested)
			deviceID, _ = requested["deviceId"].(string)
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "sessionId": session, "manualCode": "ABCD-2345", "universalLink": "https://link.nexal.systems/pair/" + session + "#" + strings.Repeat("a", 64), "pollToken": poll, "expiresAt": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "status": "waiting"})
		case "/api/v2/network-invitations/redeem":
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "sessionId": session, "status": "joining", "grantId": grant, "meshHostname": "nexal-guest-" + grant, "accessExpiresAt": now.Add(time.Hour).Format(time.RFC3339Nano), "serverNow": now.Format(time.RFC3339Nano), "inviterEmail": "owner@example.com", "accountId": "account_1", "networkId": "network_1", "deviceId": deviceID, "hostId": "host_1", "managementUrl": "https://mesh.example.com", "credential": strings.Repeat("m", 32), "hostCredential": strings.Repeat("h", 32)})
		case "/api/v2/enrollment-sessions/" + session + "/credentials/ack":
			if r.Header.Get("Authorization") != "Bearer "+poll {
				t.Error("ACK not bound to poll credential")
			}
			json.NewEncoder(w).Encode(map[string]bool{"acknowledged": true})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	cfg := config.Config{Version: 1, Coordinator: server.URL, Name: "Guest Mac", Listen: "127.0.0.1:8788", Development: true, DevSecrets: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300, Paused: true}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := redeemGuest(context.Background(), path, "ABCD2345"); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.GuestAccess == nil || saved.GuestAccess.GrantID != grant || saved.Enrollment.Status != "joining" || !saved.Paused {
		t.Fatal("not durably awaiting guard before joining")
	}
	if saved.GuestAccess.MeshHostname != "nexal-guest-"+grant {
		t.Fatal("cleanup hostname not persisted")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), strings.Repeat("h", 32)) || strings.Contains(string(raw), strings.Repeat("m", 32)) {
		t.Fatal("credentials escaped private secret store")
	}
	if len(calls) != 3 {
		t.Fatalf("unexpected network calls %v", calls)
	}
	// An uninstalled guard must block activation before any runtime command.
	if err := activateGuest(context.Background(), path); err == nil {
		t.Fatal("guest joined without expiry guard")
	}
}
