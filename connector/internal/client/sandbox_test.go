package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexal/connector/internal/sandbox"
)

func TestSandboxRunnerRoutes(t *testing.T) {
	var got []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("missing host auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"hosting":{"enabled":true},"tasks":[
{"id":"sbt_1","kind":"create","sandboxId":"sb1","hostname":"sbx-1","lifecycle":"persistent",
 "image":{"url":"https://x/y.img","sha256":"` + strings.Repeat("a", 64) + `","arch":"arm64","cloudInitFlavor":"nocloud"},
 "resources":{"cpu":2,"memoryMb":2048,"diskGb":10},
 "mesh":{"managementUrl":"https://m.example:443","setupKey":"KEY-1"},
 "sshAuthorizedKeys":["ssh-ed25519 AAAA a@b"],"sshCaPublicKey":"ssh-ed25519 AAAA ca","driveMode":"rw","driveToken":"t.k"},
{"id":"sbt_2","kind":"vnc-password","sandboxId":"sb1","password":"abcd1234"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tasks, err := c.SandboxTasks(ctx, "h1")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("%v %v", tasks, err)
	}
	a := tasks[0]
	if a.TaskID != "sbt_1" || a.Size.MemoryMB != 2048 || a.SetupKey != "KEY-1" || !a.Image.CloudInit ||
		!a.IsPersistent() || a.DriveMode != "rw" || a.SSHCAPublicKey == "" || len(a.SSHPublicKeys) != 1 {
		t.Fatalf("task decoded wrong: %+v", a)
	}
	if tasks[1].Kind != sandbox.KindVNCPassword || tasks[1].Password != "abcd1234" {
		t.Fatalf("vnc task: %+v", tasks[1])
	}
	if err := c.ReportSandboxState(ctx, "h1", sandbox.StateReport{SandboxID: "sb1", State: sandbox.StateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutSandboxHosting(ctx, "h1", sandbox.HostingConfig{Enabled: true, MaxSandboxes: 3, Placement: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportSandboxHostingState(ctx, "h1", false, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/v2/hosts/h1/sandbox-tasks", "POST /api/v2/hosts/h1/sandbox-state",
		"PUT /api/v2/hosts/h1/sandbox-hosting", "POST /api/v2/hosts/h1/sandbox-hosting-state"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("routes %v", got)
	}
	var hs struct {
		Awake     bool `json:"awake"`
		OnBattery bool `json:"onBattery"`
	}
	if err := json.Unmarshal([]byte(bodies[3]), &hs); err != nil || hs.Awake || !hs.OnBattery {
		t.Fatalf("hosting-state body %q", bodies[3])
	}
	if err := c.PutSandboxHosting(ctx, "h1", sandbox.HostingConfig{Enabled: true, MaxSandboxes: 99}); err == nil {
		t.Fatal("out-of-range maxSandboxes must be refused client-side")
	}
	if _, err := c.SandboxTasks(ctx, "../x"); err == nil {
		t.Fatal("bad host id")
	}
}

func TestSandboxListAndConnect(t *testing.T) {
	var got []string
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"sandboxes":[{"id":"sb1","futureField":1}]}`))
			return
		}
		if strings.Contains(string(b), `"vnc"`) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"sandbox_not_running"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"ssh","host":"100.64.0.2","certificate":"c"}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l, err := c.ListSandboxes(ctx, "host1")
	if err != nil || !strings.Contains(string(l), "futureField") {
		t.Fatalf("%s %v", l, err)
	}
	r, err := c.ConnectSandbox(ctx, "host1", "sb1", "ssh", "ssh-ed25519 AAAA")
	if err != nil || !strings.Contains(string(r), `"certificate":"c"`) || !strings.Contains(body, `"publicKey":"ssh-ed25519 AAAA"`) {
		t.Fatalf("%s %v %s", r, err, body)
	}
	var se *StatusError
	if _, err := c.ConnectSandbox(ctx, "host1", "sb1", "vnc", ""); !errors.As(err, &se) || se.Code != "sandbox_not_running" {
		t.Fatalf("%v", err)
	}
	if _, err := c.ConnectSandbox(ctx, "host1", "../x", "vnc", ""); err == nil {
		t.Fatal("bad id")
	}
	if _, err := c.ListSandboxes(ctx, "../x"); err == nil {
		t.Fatal("bad host id")
	}
	if _, err := c.ConnectSandbox(ctx, "", "sb1", "vnc", ""); err == nil {
		t.Fatal("bad host id")
	}
	if strings.Join(got, "|") != "GET /api/v2/hosts/host1/sandboxes|POST /api/v2/hosts/host1/sandboxes/sb1/connect|POST /api/v2/hosts/host1/sandboxes/sb1/connect" {
		t.Fatalf("%v", got)
	}
}

// A coordinator older than lanIp refuses unknown keys: the report is resent
// without it instead of failing every heartbeat. Other 400s are not retried
// without the field when there is none to drop.
func TestReportSandboxStateFallsBackWithoutLanIP(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "lanIp") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_schema","message":"Unknown field lanIp."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", true)
	if err != nil {
		t.Fatal(err)
	}
	rep := sandbox.StateReport{SandboxID: "sb1", State: sandbox.StateRunning, MeshIP: "100.64.2.9", LanIP: "192.168.68.77"}
	if err := c.ReportSandboxState(context.Background(), "h1", rep); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"lanIp":"192.168.68.77"`) ||
		strings.Contains(bodies[1], "lanIp") || !strings.Contains(bodies[1], `"meshIp":"100.64.2.9"`) {
		t.Fatalf("bodies: %q", bodies)
	}
	bodies = nil
	rep.LanIP = ""
	if err := c.ReportSandboxState(context.Background(), "h1", rep); err != nil || len(bodies) != 1 {
		t.Fatalf("%v %q", err, bodies)
	}
}
