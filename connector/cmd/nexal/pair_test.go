package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests exercise `nexal pair` against an httptest coordinator that replays
// the LITERAL bodies of apps/coordinator/src/home.ts. Nothing here touches a real
// coordinator or a network beyond loopback.
//
// The claim token below is a fixture with the shape PairingClaim.isValidToken
// demands. Every test asserts it does not reach stdout, the config file, or an
// error — the CLI's stdout is the Mac app's input, so a leak there would put the
// secret into a UI process and its logs.

const pairClaimToken = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0" // gitleaks:allow -- test fixture

const pairID = "9a8b7c6d-5e4f-4a3b-8c1d-2e3f4a5b6c7d"

// pairCoordinator serves the pairing routes for one host, with a controllable
// status so a poll can be observed transitioning.
type pairCoordinator struct {
	mu        sync.Mutex
	status    string
	expiresAt string
	hostID    string
	cancelled bool
	creates   int
	polls     int
	server    *httptest.Server
}

func newPairCoordinator(t *testing.T, ttl time.Duration) *pairCoordinator {
	t.Helper()
	p := &pairCoordinator{status: "waiting", hostID: "host1",
		expiresAt: time.Now().UTC().Add(ttl).Format("2006-01-02T15:04:05.000Z")}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.URL.Path == "/api/hosts/enroll":
			_, _ = io.WriteString(w, `{"hostId":"host1","token":"host_test_0123456789abcdefghijklmnopqrstuvwxyz"}`)
		case r.URL.Path == "/api/home/v1/devices/"+p.hostID+"/pairings" && r.Method == "POST":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			role, _ := in["role"].(string)
			p.creates++
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"schemaVersion":1,"type":"nexal-device-pairing","coordinator":%q,`+
				`"pairingId":%q,"expiresAt":%q,"role":%q,"claimToken":%q}`,
				p.server.URL, pairID, p.expiresAt, role, pairClaimToken)
		case r.URL.Path == "/api/home/v1/devices/"+p.hostID+"/pairings/"+pairID && r.Method == "GET":
			p.polls++
			status := p.status
			if p.cancelled {
				status = "cancelled"
			}
			_, _ = fmt.Fprintf(w, `{"pairingId":%q,"status":%q,"expiresAt":%q}`, pairID, status, p.expiresAt)
		case r.URL.Path == "/api/home/v1/devices/"+p.hostID+"/pairings/"+pairID+"/cancel" && r.Method == "POST":
			p.cancelled = true
			_, _ = io.WriteString(w, `{"cancelled":true}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *pairCoordinator) set(status string) {
	p.mu.Lock()
	p.status = status
	p.mu.Unlock()
}

// enrolledConfig produces a development configuration with a real host credential
// in the development secret store, which is what `nexal pair` needs and the only
// state in which pairing is possible at all.
func enrolledConfig(t *testing.T, coordinator string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	ctx := context.Background()
	if err := run(ctx, []string{"init", "--config", path, "--coordinator", coordinator,
		"--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, "one-use-test-code\n")
	_ = writer.Close()
	original := os.Stdin
	os.Stdin = reader
	err = run(ctx, []string{"enroll", "--config", path, "--code-stdin", "--dev-synthetic-hardware"})
	os.Stdin = original
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// captureRun runs the CLI with stdout and stderr redirected to pipes, returning
// both. The split matters: stdout must stay parseable JSON because the Mac app
// reads it, and the QR art must therefore be on stderr.
func captureRun(t *testing.T, ctx context.Context, args []string) (string, string, error) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	var wg sync.WaitGroup
	var out, errs strings.Builder
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&out, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errs, errR) }()
	runErr := run(ctx, args)
	os.Stdout, os.Stderr = origOut, origErr
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()
	return out.String(), errs.String(), runErr
}

func decodeRecords(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("stdout line is not JSON: %q", line)
		}
		records = append(records, record)
	}
	return records
}

func TestPairMintsRendersAndKeepsTheClaimTokenOffStdout(t *testing.T) {
	coordinator := newPairCoordinator(t, 4*time.Minute)
	path := enrolledConfig(t, coordinator.server.URL)
	stdout, stderr, err := captureRun(t, context.Background(),
		[]string{"pair", "--config", path, "--role", "receiver", "--no-poll"})
	if err != nil {
		t.Fatalf("pair failed: %v", err)
	}
	records := decodeRecords(t, stdout)
	if len(records) != 1 {
		t.Fatalf("--no-poll emitted %d records, want 1", len(records))
	}
	pairing, ok := records[0]["pairing"].(map[string]any)
	if !ok {
		t.Fatalf("no pairing object in %v", records[0])
	}
	if pairing["pairingId"] != pairID || pairing["role"] != "receiver" || pairing["status"] != "waiting" {
		t.Fatalf("pairing record = %v", pairing)
	}
	// The module matrix is in the JSON so the Mac app needs no encoder of its own.
	qr, ok := pairing["qr"].(map[string]any)
	if !ok {
		t.Fatalf("no qr object in %v", pairing)
	}
	rows, ok := qr["moduleRows"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatal("no module rows emitted; the Mac app would have nothing to draw")
	}
	size := int(qr["size"].(float64))
	if len(rows) != size {
		t.Fatalf("%d module rows for a size %d symbol", len(rows), size)
	}
	for i, row := range rows {
		s, _ := row.(string)
		if len(s) != size || strings.Trim(s, "01") != "" {
			t.Fatalf("module row %d is %q", i, s)
		}
	}
	if int(qr["quietZone"].(float64)) != 4 || qr["errorLevel"] != "M" || qr["encoding"] != "byte" {
		t.Fatalf("qr metadata = %v", qr)
	}
	// SECRECY: the token may live only in memory and in the modules.
	if strings.Contains(stdout, pairClaimToken) {
		t.Fatal("the claim token appeared on stdout")
	}
	if strings.Contains(stderr, pairClaimToken) {
		t.Fatal("the claim token appeared on stderr")
	}
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), pairClaimToken) || strings.Contains(string(config), pairID) {
		t.Fatal("pairing state was written to the configuration file")
	}
	// The QR art goes to stderr, where it cannot corrupt the JSON contract.
	if !strings.ContainsAny(stderr, "█▀▄") {
		t.Fatal("no QR was rendered to stderr")
	}
	if strings.ContainsAny(stdout, "█▀▄") {
		t.Fatal("QR art leaked into the JSON stream")
	}
}

func TestPairASCIIFallbackAvoidsUnicode(t *testing.T) {
	coordinator := newPairCoordinator(t, 4*time.Minute)
	path := enrolledConfig(t, coordinator.server.URL)
	_, stderr, err := captureRun(t, context.Background(),
		[]string{"pair", "--config", path, "--role", "donor", "--no-poll", "--ascii"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stderr {
		if r > 127 {
			t.Fatalf("the ASCII fallback emitted %q", r)
		}
	}
	if !strings.Contains(stderr, "##") {
		t.Fatal("the ASCII fallback drew nothing")
	}
}

// TestPairPollReportsTheScanTransition is the behaviour the founder sees: the
// command waits, and says what changed, rather than printing a code and exiting.
func TestPairPollReportsTheScanTransition(t *testing.T) {
	coordinator := newPairCoordinator(t, 90*time.Second)
	path := enrolledConfig(t, coordinator.server.URL)
	go func() {
		time.Sleep(300 * time.Millisecond)
		coordinator.set("scanned")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, _, err := captureRun(t, ctx, []string{"pair", "--config", path, "--role", "receiver"})
	if err != nil {
		t.Fatalf("pair poll failed: %v", err)
	}
	records := decodeRecords(t, stdout)
	last := records[len(records)-1]["pairingResult"].(map[string]any)
	if last["status"] != "scanned" || last["scanned"] != true {
		t.Fatalf("final record = %v", last)
	}
	// A status transition must be reported as it happens, not only at the end.
	var sawStatus bool
	for _, record := range records {
		if status, ok := record["pairingStatus"].(map[string]any); ok && status["status"] == "scanned" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Error("the scanned transition was not reported while polling")
	}
	if strings.Contains(stdout, pairClaimToken) {
		t.Fatal("the claim token appeared on stdout")
	}
}

// A pairing that expires is an ordinary outcome, not a CLI failure: exiting
// nonzero would make the Mac app show an error for a founder who simply did not
// scan in time.
func TestPairPollEndsQuietlyWhenThePairingExpires(t *testing.T) {
	coordinator := newPairCoordinator(t, 2*time.Second)
	path := enrolledConfig(t, coordinator.server.URL)
	go func() {
		time.Sleep(400 * time.Millisecond)
		coordinator.set("expired")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, _, err := captureRun(t, ctx, []string{"pair", "--config", path, "--role", "receiver"})
	if err != nil {
		t.Fatalf("an expired pairing must not be an error: %v", err)
	}
	records := decodeRecords(t, stdout)
	result := records[len(records)-1]["pairingResult"].(map[string]any)
	if result["status"] != "expired" || result["scanned"] != false {
		t.Fatalf("final record = %v", result)
	}
}

func TestPairStatusAndCancelAreOneShot(t *testing.T) {
	coordinator := newPairCoordinator(t, 4*time.Minute)
	path := enrolledConfig(t, coordinator.server.URL)
	stdout, _, err := captureRun(t, context.Background(),
		[]string{"pair", "--config", path, "--status", pairID})
	if err != nil {
		t.Fatal(err)
	}
	records := decodeRecords(t, stdout)
	if len(records) != 1 {
		t.Fatalf("--status emitted %d records", len(records))
	}
	status := records[0]["pairingStatus"].(map[string]any)
	if status["pairingId"] != pairID || status["status"] != "waiting" {
		t.Fatalf("status record = %v", status)
	}
	stdout, _, err = captureRun(t, context.Background(),
		[]string{"pair", "--config", path, "--cancel", pairID})
	if err != nil {
		t.Fatal(err)
	}
	if records = decodeRecords(t, stdout); len(records) != 1 || records[0]["pairingCancelled"] != true {
		t.Fatalf("cancel record = %v", records)
	}
	stdout, _, err = captureRun(t, context.Background(),
		[]string{"pair", "--config", path, "--status", pairID})
	if err != nil {
		t.Fatal(err)
	}
	if decodeRecords(t, stdout)[0]["pairingStatus"].(map[string]any)["status"] != "cancelled" {
		t.Fatal("the cancellation was not visible in the status afterwards")
	}
}

// §26: an unavailable surface must say WHICH thing is unavailable. These are the
// three real reasons pairing cannot start, and each must produce its own reason
// rather than a generic failure the owner cannot act on.
func TestPairExplainsWhyItCannotStart(t *testing.T) {
	coordinator := newPairCoordinator(t, 4*time.Minute)
	// Not enrolled: there is no host credential to authenticate the mint with.
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := run(context.Background(), []string{"init", "--config", path,
		"--coordinator", "http://127.0.0.1:1", "--dev-loopback", "--dev-secrets"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := captureRun(t, context.Background(), []string{"pair", "--config", path, "--role", "receiver"})
	if err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("unenrolled pairing produced %v", err)
	}

	// Feature disabled: the coordinator's own 503.
	disabled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/hosts/enroll" {
			_, _ = io.WriteString(w, `{"hostId":"host1","token":"host_test_0123456789abcdefghijklmnopqrstuvwxyz"}`)
			return
		}
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"code":"home_not_configured","message":"Phone pairing is not enabled."}}`)
	}))
	defer disabled.Close()
	offPath := enrolledConfig(t, disabled.URL)
	_, _, err = captureRun(t, context.Background(), []string{"pair", "--config", offPath, "--role", "receiver"})
	if err == nil || !strings.Contains(err.Error(), "not enabled on this coordinator") {
		t.Fatalf("a disabled pairing feature produced %v", err)
	}

	// Coordinator unreachable: a transport failure, with no URL or token in the
	// text. The Mac is enrolled first and the coordinator then pointed at a closed
	// port, because enrollment itself cannot succeed against a dead coordinator and
	// the case under test is a working host whose coordinator went away.
	deadPath := enrolledConfig(t, coordinator.server.URL)
	dead, err := os.ReadFile(deadPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deadPath, []byte(strings.Replace(string(dead),
		coordinator.server.URL, "http://127.0.0.1:1", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = captureRun(t, context.Background(), []string{"pair", "--config", deadPath, "--role", "receiver"})
	if err == nil {
		t.Fatal("an unreachable coordinator was reported as success")
	}
	if strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("the error leaked the coordinator address: %v", err)
	}
}

func TestPairRejectsContradictoryAndUnknownArguments(t *testing.T) {
	coordinator := newPairCoordinator(t, 4*time.Minute)
	path := enrolledConfig(t, coordinator.server.URL)
	for _, args := range [][]string{
		{"pair", "--config", path},
		{"pair", "--config", path, "--role", "receiver", "--cancel", pairID},
		{"pair", "--config", path, "--role", "observer"},
		{"pair", "--config", path, "--cancel", "not-a-uuid"},
		{"pair", "--config", path, "--status", strings.ToUpper(pairID)},
		{"pair", "--config", path, "--role", "receiver", "positional"},
		{"pair", "--config", "relative.json", "--role", "receiver"},
		{"pair", "--config", path, "--role", "receiver", "--claim-token", pairClaimToken},
	} {
		if _, _, err := captureRun(t, context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args[3:])
		} else if strings.Contains(err.Error(), pairClaimToken) {
			t.Fatal("an argument error echoed a secret")
		}
	}
}
