package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
	var got, gotPassword string
	oldSet, oldLookup := setDestination, meshLookup
	setDestination = func(_ context.Context, u string, pw []byte) error { got, gotPassword = u, string(pw); return nil }
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
		if got != "smb://"+share+"@tm-gw-1.netbird.cloud/"+share || gotPassword != "abcdefghijklmnopqrstuvwx-_12" {
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

const tmTestPassword = "abcdefghijklmnopqrstuvwx-_12"
const tmTestURL = "smb://tm33333333333343338333@tm-gw-1.netbird.cloud/tm33333333333343338333"

func stubTmutil(t *testing.T, terminal bool, run func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) error) {
	t.Helper()
	oldRun, oldTTY := runTmutil, hasTerminal
	runTmutil, hasTerminal = run, func() bool { return terminal }
	t.Cleanup(func() { runTmutil, hasTerminal = oldRun, oldTTY })
}

func TestSetDestinationKeepsPasswordOutOfArgv(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		var argv []string
		var typed []byte
		stubTmutil(t, terminal, func(_ context.Context, a []string, stdin io.Reader, stdout, _ io.Writer) error {
			argv = a
			_, _ = io.WriteString(stdout, "Destination password: ")
			var err error
			typed, err = io.ReadAll(stdin)
			return err
		})
		pw := []byte(tmTestPassword)
		if err := setDestination(context.Background(), tmTestURL, pw); err != nil {
			t.Fatal(err)
		}
		if string(typed) != tmTestPassword+"\n" {
			t.Fatalf("stdin=%q", typed)
		}
		for _, a := range argv {
			if strings.Contains(a, tmTestPassword) || strings.Contains(a, "abcdefgh") {
				t.Fatalf("password in argv: %q", argv)
			}
		}
		want := []string{"/usr/bin/sudo", "--", "/usr/bin/script", "-q", "/dev/null", "/usr/bin/tmutil", "setdestination", "-a", "-p", tmTestURL}
		if !terminal {
			want = slices.Insert(want, 1, "-n")
		}
		if !slices.Equal(argv, want) || slices.Contains(argv, "-S") {
			t.Fatalf("argv=%q", argv)
		}
		if string(pw) != tmTestPassword {
			t.Fatal("setDestination must not mutate the caller's buffer; the caller clears it")
		}
	}
}

func TestSetDestinationWithholdsPasswordUntilPrompt(t *testing.T) {
	stubTmutil(t, true, func(ctx context.Context, _ []string, stdin io.Reader, stdout, _ io.Writer) error {
		got := make(chan []byte, 1)
		go func() { b, _ := io.ReadAll(stdin); got <- b }()
		select {
		case b := <-got:
			return errors.New("stdin released before prompt: " + string(b))
		case <-time.After(50 * time.Millisecond):
		}
		_, _ = io.WriteString(stdout, "warning: "+tmTestPassword+" echoed\n")
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := setDestination(ctx, tmTestURL, []byte(tmTestPassword))
	if err == nil || !strings.Contains(err.Error(), "timed out before tmutil asked") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), tmTestPassword) {
		t.Fatal("password in error")
	}
}

func TestSetDestinationFailureRedactsOutput(t *testing.T) {
	stubTmutil(t, false, func(_ context.Context, _ []string, stdin io.Reader, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "Password:")
		b, _ := io.ReadAll(stdin)
		_, _ = stdout.Write(b)
		return errors.New("exit status 1")
	})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	err = setDestination(context.Background(), tmTestURL, []byte(tmTestPassword))
	os.Stderr = oldStderr
	_ = w.Close()
	printed, _ := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "run `nexal time-machine -connect` in Terminal") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(string(printed), tmTestPassword) || !strings.Contains(string(printed), "[redacted]") {
		t.Fatalf("stderr=%q", printed)
	}
}
