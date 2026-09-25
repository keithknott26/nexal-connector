package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

func TestStrictCLIRejectsUnknownAndUnsafeOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	for _, args := range [][]string{
		{"init", "--config", path, "--coordinator", "http://example.test"},
		{"init", "--config", path, "--coordinator", "https://example.test", "--token", "secret-must-not-echo"},
		{"init", "--config", path, "--coordinator", "http://127.0.0.1:8787", "--dev-loopback", "--dev-secrets", "--listen", "0.0.0.0:8788"},
		{"run", "--config", path, "--disable-security"},
		{"doctor", "--config", path, "--token", "secret-must-not-echo"},
		{"doctor", "--config", "relative-path"},
		{"doctor", "--config", path, "unexpected-positional-argument"},
		{"enroll", "--config", path, "--code", "secret-must-not-echo"},
		{"set-policy", "--config", path, "--memory-limit-mib", "18446744073709551615"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024", "--idle-seconds", "29"},
		// Upload throttling flags are validated before any local request, so an
		// unusable combination fails without touching the running agent.
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024",
			"--idle-seconds", "600", "--upload-mode", "fast"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024",
			"--idle-seconds", "600", "--upload-mode", "manual"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024",
			"--idle-seconds", "600", "--upload-mode", "auto", "--upload-limit-kib-per-second", "1024"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024",
			"--idle-seconds", "600", "--upload-mode", "manual", "--upload-limit-kib-per-second", "16"},
		{"set-policy", "--config", path, "--memory-limit-mib", "256", "--reserve-memory-mib", "1024",
			"--idle-seconds", "600", "--upload-mode", "manual", "--upload-limit-kib-per-second", "18446744073709551615"},
	} {
		err := run(context.Background(), args)
		if err == nil {
			t.Fatalf("unsafe flags accepted: %v", args[:1])
		}
		if strings.Contains(err.Error(), "secret-must-not-echo") {
			t.Fatal("secret argument echoed")
		}
	}
}

func TestDoctorCommandProducesSanitizedMissingConfigReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-path-must-not-echo", "config.json")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; _ = reader.Close(); _ = writer.Close() }()
	err = run(context.Background(), []string{"doctor", "--config", path})
	_ = writer.Close()
	os.Stdout = original
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil || strings.Contains(string(output), path) {
		t.Fatal("private path disclosed or report unreadable")
	}
	var report struct {
		SchemaVersion   int  `json:"schemaVersion"`
		ProductionReady bool `json:"productionReady"`
		Checks          []struct{ ID, Status string }
	}
	if json.Unmarshal(output, &report) != nil || report.SchemaVersion != 1 || report.ProductionReady {
		t.Fatal("invalid report")
	}
	for _, check := range report.Checks {
		if check.ID == "configuration" && check.Status == "blocked" {
			return
		}
	}
	t.Fatal("missing blocked configuration check")
}

// Complete CLI -> credential store -> host REST -> local control -> workload ->
// completion test. All listeners are ephemeral loopback; no external service.
func TestDevelopmentPrivateLoopEndToEnd(t *testing.T) {
	const hostToken = "host_test_0123456789abcdefghijklmnopqrstuvwxyz"
	var mu sync.Mutex
	ownerActive := true
	completed := 0
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		respond := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.URL.Path != "/api/hosts/enroll" && r.Header.Get("Authorization") != "Bearer "+hostToken {
			t.Error("wrong scoped host token")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/hosts/enroll":
			var in client.Enrollment
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Code != "one-use-test-code" || in.MemoryBytes != 8<<30 {
				t.Error("enrollment input mismatch")
				w.WriteHeader(400)
				return
			}
			respond(client.Identity{HostID: "host1", Token: hostToken})
		case "/api/hosts/host1/heartbeat":
			var in client.Heartbeat
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.PQ.Verified {
				t.Error("invalid heartbeat")
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			ownerActive = in.OwnerActive
			mu.Unlock()
			respond(map[string]any{"ok": true, "leaseSeconds": 60})
		case "/api/hosts/host1/next":
			mu.Lock()
			active := ownerActive
			mu.Unlock()
			if active {
				respond(map[string]any{"attempt": nil})
				return
			}
			respond(map[string]any{"attempt": client.Attempt{ID: "attempt1", JobID: "job1", HostID: "host1",
				Template: "monte-carlo-pi-v1", Samples: 100000, LeaseExpiresAt: time.Now().Add(time.Minute)}})
		case "/api/attempts/attempt1/complete":
			var in struct {
				Result       client.Result `json:"result"`
				UsageSeconds float64       `json:"usageSeconds"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Result.Samples != 100000 || in.Result.Pi < 3 || in.Result.Pi > 3.3 {
				t.Error("invalid fixed workload result")
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			completed++
			mu.Unlock()
			respond(map[string]bool{"accepted": true})
		case "/api/v2/hosts/events", "/api/v2/hosts/wake-info":
			// `run` now opens the live presence stream and reports wake info.
			// This fake coordinator predates both, which is exactly the older
			// coordinator the agent must tolerate quietly: 404, and carry on.
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer coordinator.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	local := listener.Addr().String()
	_ = listener.Close()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	ctx := context.Background()
	if err := run(ctx, []string{"init", "--config", path, "--coordinator", coordinator.URL, "--listen", local, "--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	// Enrollment reads only stdin; never argv/environment.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, "one-use-test-code\n")
	_ = writer.Close()
	originalStdin := os.Stdin
	os.Stdin = reader
	enrollErr := run(ctx, []string{"enroll", "--config", path, "--code-stdin", "--dev-synthetic-hardware"})
	os.Stdin = originalStdin
	_ = reader.Close()
	if enrollErr != nil {
		t.Fatal(enrollErr)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("load after enrollment: %v", err)
	}
	if c.HostID != "host1" {
		t.Fatalf("host id after enrollment = %q, want host1", c.HostID)
	}
	// Paused now defaults to FALSE. This previously asserted true, which was the old
	// init default; contributing is the purpose of joining, so a fresh configuration no
	// longer starts withholding. Split out from the combined check above so a future
	// failure names which fact broke instead of "config mismatch".
	if c.Paused {
		t.Error("a freshly initialized configuration should not start paused")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- run(runCtx, []string{"run", "--config", path, "--dev-private-pull", "--dev-assume-idle"})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent shutdown timeout")
		}
	}()
	secrets, _ := config.NewSecrets(path, c)
	admin, err := secrets.Get(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	command := func(method, route string) int {
		r, _ := http.NewRequest(method, "http://"+local+route, nil)
		r.Header.Set("Authorization", "Bearer "+admin)
		resp, err := httpClient.Do(r)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for deadline := time.Now().Add(3 * time.Second); command("GET", "/v1/status") != 200; {
		if time.Now().After(deadline) {
			t.Fatal("local API did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if command("POST", "/v1/resume") != 200 {
		t.Fatal("resume failed")
	}
	for deadline := time.Now().Add(6 * time.Second); ; {
		mu.Lock()
		n := completed
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private job did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if command("POST", "/v1/pause") != 200 {
		t.Fatal("pause failed")
	}
	if err := run(ctx, []string{"set-policy", "--config", path, "--memory-limit-mib", "128", "--reserve-memory-mib", "512", "--idle-seconds", "600"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"policy", "--config", path}); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(path)
	if err != nil || saved.MemoryLimitBytes != 128<<20 || saved.ReserveMemoryBytes != 512<<20 || saved.IdleSeconds != 600 || !saved.Paused {
		t.Fatal("CLI policy change did not preserve paused state and persist limits")
	}
	// Omitting the upload flags inherits auto, which is what an existing script
	// or the already-built Swift UI sends.
	if saved.ResourcePolicy().UploadMode != config.UploadModeAuto {
		t.Fatalf("a three-flag set-policy produced mode %q, want auto", saved.ResourcePolicy().UploadMode)
	}
	if err := run(ctx, []string{"set-policy", "--config", path, "--memory-limit-mib", "128",
		"--reserve-memory-mib", "512", "--idle-seconds", "600", "--upload-mode", "manual",
		"--upload-limit-kib-per-second", "512"}); err != nil {
		t.Fatal(err)
	}
	saved, err = config.Load(path)
	if err != nil || saved.UploadMode != config.UploadModeManual || saved.UploadLimitBytesPerSecond != 512<<10 {
		t.Fatalf("CLI upload throttle did not persist: %+v", err)
	}
	if !saved.Paused {
		t.Fatal("an upload policy change resumed a paused host")
	}
	if command("POST", "/v1/attempts") != 403 {
		t.Fatal("unverified dispatch enabled")
	}
	mu.Lock()
	n := completed
	mu.Unlock()
	if n != 1 {
		t.Fatal("duplicate completion")
	}
	if err := run(ctx, []string{"self-test", "--config", path}); err == nil {
		t.Fatal("offline self-test bypassed active process lock")
	}
}

func TestOfflineSelfTestNoEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path, "--coordinator", "http://127.0.0.1:1", "--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"self-test", "--config", path, "--samples", "10000"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"self-test", "--config", path, "--samples", "1000001"}); err == nil {
		t.Fatal("unbounded self-test")
	}
}

// Static cross-VLAN peer CLI. It edits the config file, so these assertions read
// the file back: the whole point of the command is what ends up persisted.
func TestStaticPeersCommandAddsListsAndRemoves(t *testing.T) {
	const fingerprint = "aa11223344556677889900112233445566778899001122334455667788990011"
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path,
		"--coordinator", "http://127.0.0.1:1", "--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	// An owner-typed RFC1918 address on ANOTHER subnet is accepted, because the
	// peer transport already permits a routed private address; this change adds
	// no relaxation. See TRANSPORT-NAT-DESIGN.md.
	if err := run(context.Background(), []string{"static-peers", "add", "--config", path,
		"--endpoint", "https://10.20.0.5:8443", "--fingerprint", fingerprint, "--label", "studio vlan 20"}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.StaticPeers) != 1 || c.StaticPeers[0].Endpoint != "https://10.20.0.5:8443" ||
		c.StaticPeers[0].Fingerprint != fingerprint || c.StaticPeers[0].Label != "studio vlan 20" {
		t.Fatalf("stored = %+v", c.StaticPeers)
	}
	if err := run(context.Background(), []string{"static-peers", "list", "--config", path}); err != nil {
		t.Fatal(err)
	}
	// Adding the same device twice must be refused rather than silently
	// overwriting where a dial goes.
	if err := run(context.Background(), []string{"static-peers", "add", "--config", path,
		"--endpoint", "https://10.20.0.9:8443", "--fingerprint", fingerprint}); err == nil {
		t.Fatal("duplicate fingerprint accepted")
	}
	if err := run(context.Background(), []string{"static-peers", "remove", "--config", path,
		"--fingerprint", fingerprint}); err != nil {
		t.Fatal(err)
	}
	if c, err = config.Load(path); err != nil || len(c.StaticPeers) != 0 {
		t.Fatalf("static peers = %+v err = %v", c.StaticPeers, err)
	}
}

func TestStaticPeersCommandRefusesUnsafeInput(t *testing.T) {
	const fingerprint = "aa11223344556677889900112233445566778899001122334455667788990011"
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path,
		"--coordinator", "http://127.0.0.1:1", "--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		// A PUBLIC endpoint is refused: the private-IP rule is not relaxed for
		// cross-VLAN, and nothing here opens a path to the internet.
		{"static-peers", "add", "--config", path, "--endpoint", "https://203.0.113.9:8443", "--fingerprint", fingerprint},
		// A hostname cannot work: the peer transport performs no DNS lookup.
		{"static-peers", "add", "--config", path, "--endpoint", "https://peer.example:8443", "--fingerprint", fingerprint},
		{"static-peers", "add", "--config", path, "--endpoint", "http://10.20.0.5:8443", "--fingerprint", fingerprint},
		{"static-peers", "add", "--config", path, "--endpoint", "https://10.20.0.5", "--fingerprint", fingerprint},
		// An address without a fingerprint would be "trust whoever answers".
		{"static-peers", "add", "--config", path, "--endpoint", "https://10.20.0.5:8443"},
		{"static-peers", "add", "--config", path, "--fingerprint", fingerprint},
		{"static-peers", "add", "--config", path, "--endpoint", "https://10.20.0.5:8443", "--fingerprint", "short"},
		{"static-peers", "remove", "--config", path, "--endpoint", "https://10.20.0.5:8443"},
		{"static-peers", "remove", "--config", path, "--fingerprint", fingerprint},
		{"static-peers", "trust-all", "--config", path},
		{"static-peers", "list", "--config", path, "--endpoint", "https://10.20.0.5:8443"},
		{"static-peers"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("accepted unsafe static-peers input: %v", args[1:])
		}
	}
	c, err := config.Load(path)
	if err != nil || len(c.StaticPeers) != 0 {
		t.Fatalf("a refused command must persist nothing: %+v", c.StaticPeers)
	}
}

// doctor --stun is opt-in, and plain doctor must not send a packet. This test
// only asserts the flag parses and that the default path stays offline; the STUN
// behaviour itself is tested offline in internal/stun and internal/diagnostics.
func TestDoctorSTUNFlagIsOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	err = run(context.Background(), []string{"doctor", "--config", path})
	_ = writer.Close()
	os.Stdout = original
	if err != nil {
		t.Fatal(err)
	}
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	var report struct {
		NetworkContacted bool `json:"networkContacted"`
		NAT              any  `json:"nat"`
	}
	if json.Unmarshal(output, &report) != nil || report.NetworkContacted || report.NAT != nil {
		t.Fatalf("default doctor must stay offline and report no NAT block: %s", output)
	}
	if err := run(context.Background(), []string{"doctor", "--config", path, "--stun", "--extra"}); err == nil {
		t.Fatal("unknown flag accepted alongside --stun")
	}
}

// The sharing default, asserted directly rather than as a side effect of the end-to-end
// test. The Mac app previously showed an "I agree to share resources" checkbox because
// init wrote Paused: true, so a Mac that had just joined in order to share was withholding
// until the owner turned it on.
func TestInitDefaultsToContributing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path,
		"--coordinator", "http://127.0.0.1:8787", "--name", "test mac",
		"--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paused {
		t.Error("init wrote paused=true; joining should contribute by default")
	}
}

// The opt-out must still exist: a machine can join without contributing.
func TestInitPausedFlagStillWithholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path,
		"--coordinator", "http://127.0.0.1:8787", "--name", "test mac",
		"--dev-loopback", "--dev-secrets", "--paused"}); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Paused {
		t.Error("--paused did not withhold contribution")
	}
}
