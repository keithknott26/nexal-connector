package sharing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stub replaces the three package seams for one test and restores them.
type stub struct {
	// open is the set of ports that answer. Commands may add to it.
	open map[string]bool
	// calls records every privileged command as "name arg arg".
	calls []string
	// fail maps a command prefix to the error it returns.
	fail map[string]error
	// opens maps a command prefix to the port it makes answer.
	opens map[string]string
	uid   int
	goos  string
}

func install(t *testing.T, s *stub) {
	t.Helper()
	oldListening, oldRun, oldEUID, oldPlatform := listening, runRoot, euid, platform
	t.Cleanup(func() { listening, runRoot, euid, platform = oldListening, oldRun, oldEUID, oldPlatform })
	if s.open == nil {
		s.open = map[string]bool{}
	}
	listening = func(_ context.Context, tcpPort string) bool { return s.open[tcpPort] }
	runRoot = func(_ context.Context, name string, args ...string) error {
		line := strings.Join(append([]string{name}, args...), " ")
		s.calls = append(s.calls, line)
		for prefix, err := range s.fail {
			if strings.HasPrefix(line, prefix) {
				return err
			}
		}
		for prefix, tcpPort := range s.opens {
			if strings.HasPrefix(line, prefix) {
				s.open[tcpPort] = true
			}
		}
		return nil
	}
	euid = func() int { return s.uid }
	platform = s.goos
}

func TestValidAndServicesCoverExactlyThreeIdentifiers(t *testing.T) {
	for _, id := range []string{ScreenSharing, FileSharing, SecureShell} {
		if !Valid(id) {
			t.Fatalf("%q must be valid", id)
		}
	}
	for _, id := range []string{"", "screen sharing", "ssh", "SECURE-SHELL", "vnc"} {
		if Valid(id) {
			t.Fatalf("%q must not be valid", id)
		}
	}
	if got := Services(); len(got) != 3 {
		t.Fatalf("Services() = %v, want three identifiers", got)
	}
	for _, id := range Services() {
		if !Valid(id) || port(id) == "" {
			t.Fatalf("Services() returned %q with no port", id)
		}
	}
}

func TestStatusReportsEveryServiceFromThePortProbe(t *testing.T) {
	s := &stub{open: map[string]bool{"22": true, "445": true}, uid: 0, goos: "darwin"}
	install(t, s)
	state, err := Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{SecureShell: true, FileSharing: true, ScreenSharing: false}
	for service, on := range want {
		if state[service] != on {
			t.Fatalf("%s = %v, want %v", service, state[service], on)
		}
	}
	if len(state) != 3 {
		t.Fatalf("status has %d entries, want 3", len(state))
	}
	if len(s.calls) != 0 {
		t.Fatalf("Status must spawn nothing, ran %v", s.calls)
	}
}

func TestStatusReportsNoMapWhenTheContextEnded(t *testing.T) {
	install(t, &stub{uid: 0, goos: "darwin"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := Status(ctx)
	if err == nil || state != nil {
		t.Fatalf("cancelled Status = %v, %v; want nil map and an error", state, err)
	}
}

func TestEnableRefusesFileSharingBeforeTouchingAnything(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin"}
	install(t, s)
	if err := Enable(context.Background(), FileSharing); !errors.Is(err, ErrFileSharingManual) {
		t.Fatalf("file sharing enable = %v, want ErrFileSharingManual", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("file sharing must run no commands, ran %v", s.calls)
	}
}

func TestEnableRefusesUnknownServiceAndNonRootAndNonDarwin(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin"}
	install(t, s)
	if err := Enable(context.Background(), "printer-sharing"); !errors.Is(err, ErrUnknownService) {
		t.Fatalf("unknown service = %v, want ErrUnknownService", err)
	}
	s2 := &stub{uid: 501, goos: "darwin"}
	install(t, s2)
	if err := Enable(context.Background(), ScreenSharing); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("non-root enable = %v, want ErrNotRoot", err)
	}
	if len(s2.calls) != 0 {
		t.Fatalf("non-root enable must run no commands, ran %v", s2.calls)
	}
	s3 := &stub{uid: 0, goos: "linux"}
	install(t, s3)
	if err := Enable(context.Background(), ScreenSharing); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("non-darwin enable = %v, want ErrUnsupportedPlatform", err)
	}
	if len(s3.calls) != 0 {
		t.Fatalf("non-darwin enable must run no commands, ran %v", s3.calls)
	}
}

func TestEnableScreenSharingUsesLaunchctlAndWaitsForThePort(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin",
		opens: map[string]string{"/bin/launchctl bootstrap": "5900"}}
	install(t, s)
	if err := Enable(context.Background(), ScreenSharing); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/bin/launchctl enable system/com.apple.screensharing",
		"/bin/launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist",
	}
	if len(s.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", s.calls, want)
	}
	for i := range want {
		if s.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, s.calls[i], want[i])
		}
	}
}

func TestEnableSecureShellPrefersSystemsetupAndFallsBackToLaunchctl(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin", opens: map[string]string{"/usr/sbin/systemsetup": "22"}}
	install(t, s)
	if err := Enable(context.Background(), SecureShell); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || !strings.HasPrefix(s.calls[0], "/usr/sbin/systemsetup -f -setremotelogin on") {
		t.Fatalf("calls = %v, want systemsetup only", s.calls)
	}

	s2 := &stub{uid: 0, goos: "darwin",
		fail:  map[string]error{"/usr/sbin/systemsetup": errors.New("requires Full Disk Access")},
		opens: map[string]string{"/bin/launchctl bootstrap": "22"}}
	install(t, s2)
	if err := Enable(context.Background(), SecureShell); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/sbin/systemsetup -f -setremotelogin on",
		"/bin/launchctl enable system/com.openssh.sshd",
		"/bin/launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist",
	}
	if len(s2.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", s2.calls, want)
	}
	for i := range want {
		if s2.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, s2.calls[i], want[i])
		}
	}
}

func TestEnableIsANoOpWhenTheServiceIsAlreadyListening(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin", open: map[string]bool{"5900": true}}
	install(t, s)
	if err := Enable(context.Background(), ScreenSharing); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("an already-listening service must not be bounced, ran %v", s.calls)
	}
}

func TestEnableFailsWhenThePortNeverComesUp(t *testing.T) {
	// Every command "succeeds" and nothing starts listening: the honest answer
	// is a failure, not the zero exit status.
	s := &stub{uid: 0, goos: "darwin"}
	install(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // keeps the ten-second wait from running for real
	err := Enable(ctx, ScreenSharing)
	if !errors.Is(err, ErrNotListening) {
		t.Fatalf("enable = %v, want ErrNotListening", err)
	}
}

func TestEnableReportsTheCommandErrorWhenThePortStaysClosed(t *testing.T) {
	s := &stub{uid: 0, goos: "darwin",
		fail: map[string]error{"/bin/launchctl bootstrap": errors.New("Bootstrap failed: 5: Input/output error")}}
	install(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Enable(ctx, ScreenSharing)
	if err == nil || !strings.Contains(err.Error(), "Bootstrap failed") {
		t.Fatalf("enable = %v, want the command's own output", err)
	}
}

func TestLabelNamesEveryServiceForTheOwner(t *testing.T) {
	for _, service := range Services() {
		if Label(service) == service {
			t.Fatalf("%s has no owner-facing label", service)
		}
	}
}

func TestSortedServicesIsStable(t *testing.T) {
	got := SortedServices(map[string]bool{SecureShell: true, ScreenSharing: false, FileSharing: true})
	want := []string{FileSharing, ScreenSharing, SecureShell}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortedServices = %v, want %v", got, want)
		}
	}
}
