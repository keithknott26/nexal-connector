package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	oldSet, oldLookup, oldConfigured, oldWait := setDestination, meshLookup, destinationConfigured, destinationVerifyWait
	added := false
	setDestination = func(_ context.Context, u string, pw []byte) error {
		got, gotPassword, added = u, string(pw), true
		return nil
	}
	destinationConfigured = func(string, string) bool { return added }
	destinationVerifyWait = 0
	meshLookup = func(string) ([]string, error) { return []string{"100.101.2.3"}, nil }
	defer func() {
		setDestination, meshLookup, destinationConfigured, destinationVerifyWait = oldSet, oldLookup, oldConfigured, oldWait
	}()
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
		// Already configured: no second privileged call.
		got = ""
		if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"}); err != nil || got != "" {
			t.Fatalf("configured destination must not re-run tmutil: %v %q", err, got)
		}
		// tmutil reported success but Time Machine does not list the share.
		added = false
		setDestination = func(_ context.Context, u string, pw []byte) error { got = u; return nil }
		if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"}); err == nil {
			t.Fatal("unverified destination must not report success")
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
	oldProbe := probeTimeMachineAccess
	probeTimeMachineAccess = func(context.Context) ([]byte, error) { return nil, nil }
	t.Cleanup(func() { probeTimeMachineAccess = oldProbe })
	oldRun, oldTTY, oldElevated := runTmutil, hasTerminal, elevatedPromptAvailable
	runTmutil, hasTerminal = run, func() bool { return terminal }
	elevatedPromptAvailable = func() bool { return false }
	t.Cleanup(func() { runTmutil, hasTerminal, elevatedPromptAvailable = oldRun, oldTTY, oldElevated })
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
		want := []string{"/usr/bin/sudo", "--", "/usr/bin/expect", "-c", tmExpectScript(tmutilPath, "setdestination", "-a", "-p", tmTestURL)}
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

func TestElevatedScriptCarriesNoSecretAndQuotes(t *testing.T) {
	script := elevatedScript(tmTestURL, "/tmp/d'x/in", "/tmp/d'x/out")
	for _, want := range []string{
		`do shell script "/usr/bin/expect -c `,
		`< '/tmp/d'\\''x/in' > '/tmp/d'\\''x/out' 2>&1"`,
		"with administrator privileges with prompt ",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script %q lacks %q", script, want)
		}
	}
	if strings.Contains(script, tmTestPassword) {
		t.Fatal("password in script")
	}
}

func TestSetDestinationUsesAdminDialogWithoutTerminal(t *testing.T) {
	stubTmutil(t, false, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("sudo path used although the administrator dialog is available")
		return nil
	})
	elevatedPromptAvailable = func() bool { return true }
	oldElevated := runElevated
	t.Cleanup(func() { runElevated = oldElevated })
	var typed []byte
	runElevated = func(_ context.Context, url string, stdin io.Reader, stdout io.Writer) error {
		if url != tmTestURL {
			t.Fatalf("url=%q", url)
		}
		_, _ = io.WriteString(stdout, "Destination password: ")
		typed, _ = io.ReadAll(stdin)
		return nil
	}
	if err := setDestination(context.Background(), tmTestURL, []byte(tmTestPassword)); err != nil {
		t.Fatal(err)
	}
	if string(typed) != tmTestPassword+"\n" {
		t.Fatalf("stdin=%q", typed)
	}
	runElevated = func(context.Context, string, io.Reader, io.Writer) error { return errAdminCancelled }
	if err := setDestination(context.Background(), tmTestURL, []byte(tmTestPassword)); !errors.Is(err, errAdminCancelled) {
		t.Fatalf("err=%v", err)
	}
}

func TestDestinationListedMatchesGatewayShareOnly(t *testing.T) {
	info := "====\nName          : neXal Time Machine\nKind          : Network\nURL           : smb://tmabc@gw-us-east-1.netbird.selfhosted/tmabc\nID            : 1\n"
	if !destinationListed(info, "gw-us-east-1.netbird.selfhosted", "tmabc") {
		t.Fatal("configured destination not recognised")
	}
	for _, c := range [][2]string{{"gw-us-east-1.netbird.selfhosted", "tmother"}, {"other-gw", "tmabc"}} {
		if destinationListed(info, c[0], c[1]) {
			t.Fatalf("false match for %v", c)
		}
	}
}

func TestTimeMachineTerminalWrapperFromPipe(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS terminal wrapper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := tmExpectScript("/bin/sh", "-c", `stty -echo; printf "Password: "; read p; test "$p" = "synthetic-test"`)
	cmd := exec.CommandContext(ctx, expectPath, "-c", script)
	cmd.Stdin = strings.NewReader("synthetic-test\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Password:") || strings.Contains(string(out), "synthetic-test") {
		t.Fatalf("terminal wrapper failed: %v %q", err, out)
	}
	cmd = exec.CommandContext(ctx, expectPath, "-c", tmExpectScript("/bin/sh", "-c", `stty -echo; printf "Password: "; read p; exit 7`))
	cmd.Stdin = strings.NewReader("synthetic-test\n")
	out, err = cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || strings.Contains(string(out), "synthetic-test") {
		t.Fatalf("child failure was hidden: %v %q", err, out)
	}

}

func TestSetDestinationOnlyRequestsFullDiskAccessForExplicitDenial(t *testing.T) {
	for _, tc := range []struct {
		output          string
		wantsPermission bool
	}{
		{"tmutil: setdestination requires Full Disk Access privileges.", true},
		{"Failed to connect to backup destination", false},
		{"Authentication failed", false},
	} {
		t.Run(tc.output, func(t *testing.T) {
			stubTmutil(t, true, func(_ context.Context, _ []string, _ io.Reader, stdout, _ io.Writer) error {
				_, _ = io.WriteString(stdout, tc.output)
				return errors.New("exit status 1")
			})
			err := setDestination(context.Background(), tmTestURL, []byte(tmTestPassword))
			if err == nil || strings.Contains(err.Error(), "Full Disk Access") != tc.wantsPermission {
				t.Fatalf("unexpected permission classification: %v", err)
			}
		})
	}
}

func TestTimeMachinePreflightDenialDoesNotElevate(t *testing.T) {
	stubTmutil(t, false, func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("permission denial reached administrator prompt")
		return nil
	})
	probeTimeMachineAccess = func(context.Context) ([]byte, error) {
		return []byte("tmutil: listbackups requires Full Disk Access privileges."), errors.New("exit 1")
	}
	err := setDestination(context.Background(), tmTestURL, []byte(tmTestPassword))
	if err == nil || err.Error() != timeMachineAccessError {
		t.Fatalf("unexpected preflight: %v", err)
	}
}

func TestTimeMachinePreflightNoBackupsIsNotPermissionDenial(t *testing.T) {
	old := probeTimeMachineAccess
	defer func() { probeTimeMachineAccess = old }()
	probeTimeMachineAccess = func(context.Context) ([]byte, error) {
		return []byte("No machine directory found for host."), errors.New("exit 1")
	}
	if err := preflightTimeMachineAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTimeMachineCredentialRevealRequiresEnabledAssignment(t *testing.T) {
	enabled := true
	calls := 0
	const share = "tm33333333333343338333"
	dest := map[string]any{"protocol": "smb", "host": "100.86.173.7", "port": 445, "share": share, "username": share}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			json.NewEncoder(w).Encode(map[string]any{"enabled": enabled, "role": "client", "serviceState": "ready", "destination": dest, "credentialEndpoint": "/time-machine/credentials", "credentialsIncluded": false})
		} else if strings.HasSuffix(r.URL.Path, "/credentials") {
			calls++
			value := map[string]any{"password": tmTestPassword, "url": "ignored"}
			for k, v := range dest {
				value[k] = v
			}
			json.NewEncoder(w).Encode(value)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	path := writeTimeMachineTestConfig(t, s.URL)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = timeMachineCommand(context.Background(), []string{"--config", path, "--reveal-credentials"})
	w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if json.Unmarshal(data, &value) != nil || value["password"] != tmTestPassword || value["username"] != share || calls != 1 {
		t.Fatal("reveal did not return the assigned credential")
	}
	enabled = false
	if err = timeMachineCommand(context.Background(), []string{"--config", path, "--reveal-credentials"}); err == nil || calls != 1 {
		t.Fatal("disabled assignment minted credentials")
	}
}

func TestTimeMachineConnectEnrollsThenWaitsForDestination(t *testing.T) {
	const share = "tm44444444444444448444"
	dest := map[string]any{"protocol": "smb", "host": "tm-gw-1.netbird.cloud", "port": 445, "share": share, "username": share}
	doc := func(enabled bool) map[string]any {
		d := map[string]any{"enabled": enabled, "role": "client", "serviceState": "provisioning", "computerId": "c1", "networkId": "n1",
			"destination": nil, "credentialEndpoint": "/api/v2/devices/host1/time-machine/credentials", "credentialsIncluded": false,
			"quotaBytes": 1 << 40, "refreshAfterSeconds": 60}
		if enabled {
			d["destination"] = dest
		}
		return d
	}
	enrolls, reads := 0, 0
	plan := "paid"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/devices/host1/time-machine/enroll":
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			if plan != "paid" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"paid_plan_required","message":"x"}}`))
				return
			}
			enrolls++
			_ = json.NewEncoder(w).Encode(doc(false)) // gateway share still provisioning
		case "/api/v2/devices/host1/time-machine/config":
			reads++
			_ = json.NewEncoder(w).Encode(doc(reads >= 2))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	p := writeTimeMachineTestConfig(t, s.URL)
	oldLookup, oldConfigured, oldPoll := meshLookup, destinationConfigured, enrollPoll
	meshLookup = func(string) ([]string, error) { return []string{"100.101.2.3"}, nil }
	destinationConfigured = func(string, string) bool { return true } // stop before tmutil
	enrollPoll = time.Millisecond
	defer func() { meshLookup, destinationConfigured, enrollPoll = oldLookup, oldConfigured, oldPoll }()

	if err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"}); err != nil {
		t.Fatal(err)
	}
	if enrolls != 1 || reads != 2 {
		t.Fatalf("want one enrollment then polling until the destination appears; enrolls=%d reads=%d", enrolls, reads)
	}
	// A plain status check never enrolls.
	if err := timeMachineCommand(context.Background(), []string{"-config", p}); err != nil || enrolls != 1 {
		t.Fatalf("status check must not enroll: %v enrolls=%d", err, enrolls)
	}
	plan = "free"
	err := timeMachineCommand(context.Background(), []string{"-config", p, "-connect"})
	if err == nil || !strings.Contains(err.Error(), "subscription") {
		t.Fatalf("unpaid enrollment must explain the subscription requirement: %v", err)
	}
}
