package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	gstatus "google.golang.org/grpc/status"

	"github.com/netbirdio/netbird/client/proto"
)

// The failure this repairs, as the CLI actually receives it.
var stuckLoginFailure = errors.New("rpc error: code = DeadlineExceeded desc = create connection: dial context: context deadline exceeded")

func TestNexalStuckLoginIsToldApartFromAnAnswer(t *testing.T) {
	for _, err := range []error{
		stuckLoginFailure,
		gstatus.Error(codes.DeadlineExceeded, "create connection"),
		gstatus.Error(codes.Unavailable, "management is restarting"),
		context.DeadlineExceeded,
		errors.New("secure networking service did not finish authentication; restart the neXal networking service and retry"),
	} {
		if !isStuckLogin(err) {
			t.Errorf("a login that never reached a decision was not recognised: %v", err)
		}
	}
	// Management answering is an answer, however unwelcome: repairing the
	// daemon over it would hide a real refusal.
	for _, err := range []error{
		nil,
		gstatus.Error(codes.PermissionDenied, "peer is not allowed"),
		gstatus.Error(codes.InvalidArgument, "invalid setup key"),
		gstatus.Error(codes.NotFound, "peer not found"),
		errors.New("update settings are disabled"),
	} {
		if isStuckLogin(err) {
			t.Errorf("an answer from management was treated as a stuck login: %v", err)
		}
	}
}

// Rung 1: dropping the daemon's connection is enough, and the service is left
// alone.
func TestNexalStuckLoginIsRepairedByReconnecting(t *testing.T) {
	daemon := &fakeDaemon{}
	restarts := withFastRepair(t, func(context.Context, *cobra.Command) error {
		t.Error("the service was restarted for a login that reconnecting fixed")
		return nil
	})

	login := failingLogin(0) // every attempt after the caller's own succeeds
	if err := retryLoginAfterHealing(context.Background(), quietCommand(), daemon, login.attempt, stuckLoginFailure); err != nil {
		t.Fatalf("repair reported a failure: %v", err)
	}
	if daemon.downs != 1 {
		t.Errorf("the daemon's connection was dropped %d times, want 1", daemon.downs)
	}
	if login.calls != 1 {
		t.Errorf("the repair made %d login attempts, want 1", login.calls)
	}
	if *restarts != 0 {
		t.Errorf("the service was restarted %d times", *restarts)
	}
}

// Rung 2: reconnecting did not help, so the service is restarted and the daemon
// is waited for before the last attempt.
func TestNexalStuckLoginFallsBackToRestartingTheService(t *testing.T) {
	daemon := &fakeDaemon{statusFailures: 2}
	restarts := withFastRepair(t, nil)

	login := failingLogin(1) // the attempt after reconnecting still fails
	if err := retryLoginAfterHealing(context.Background(), quietCommand(), daemon, login.attempt, stuckLoginFailure); err != nil {
		t.Fatalf("repair reported a failure: %v", err)
	}
	if *restarts != 1 {
		t.Errorf("the service was restarted %d times, want 1", *restarts)
	}
	if daemon.statuses < 3 {
		t.Errorf("the daemon was polled %d times; the repair did not wait for it to come back", daemon.statuses)
	}
	if login.calls != 2 {
		t.Errorf("the repair made %d login attempts, want 2 (before and after the restart)", login.calls)
	}
}

// A refusal arriving during the repair is the answer, and it comes back
// unchanged instead of being buried under a restart.
func TestNexalStuckLoginStopsAtARefusal(t *testing.T) {
	daemon := &fakeDaemon{}
	restarts := withFastRepair(t, func(context.Context, *cobra.Command) error {
		t.Error("the service was restarted over a refusal from management")
		return nil
	})

	refusal := gstatus.Error(codes.PermissionDenied, "peer is not allowed")
	err := retryLoginAfterHealing(context.Background(), quietCommand(), daemon,
		func() error { return refusal }, stuckLoginFailure)
	if !errors.Is(err, refusal) {
		t.Fatalf("the refusal was not returned: %v", err)
	}
	if *restarts != 0 {
		t.Errorf("the service was restarted %d times", *restarts)
	}
}

// A service that cannot be restarted (no privileges) reports the original
// failure, with the reason the repair could not finish.
func TestNexalStuckLoginReportsAnUnrepairableService(t *testing.T) {
	daemon := &fakeDaemon{}
	withFastRepair(t, func(context.Context, *cobra.Command) error {
		return errors.New("needs administrator rights")
	})

	err := retryLoginAfterHealing(context.Background(), quietCommand(), daemon,
		func() error { return stuckLoginFailure }, stuckLoginFailure)
	if err == nil {
		t.Fatal("an unrepairable service reported success")
	}
	if !errors.Is(err, stuckLoginFailure) {
		t.Errorf("the original failure was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "needs administrator rights") {
		t.Errorf("the reason the repair could not finish was lost: %v", err)
	}
}

// A daemon that never answers after the restart is reported as such, rather
// than looking like a login failure.
func TestNexalStuckLoginReportsADaemonThatDoesNotComeBack(t *testing.T) {
	daemon := &fakeDaemon{statusFailures: 1000}
	withFastRepair(t, nil)

	err := retryLoginAfterHealing(context.Background(), quietCommand(), daemon,
		func() error { return stuckLoginFailure }, stuckLoginFailure)
	if err == nil || !strings.Contains(err.Error(), "did not come back") {
		t.Fatalf("a daemon that never answered was not reported: %v", err)
	}
}

func TestNexalManualRestartInstructionsQuoteThePath(t *testing.T) {
	text := manualRestartInstructions("/Applications/neXal-Connector.app/Contents/Helpers/nexal-network")
	if !strings.Contains(text, "\"/Applications/neXal-Connector.app/Contents/Helpers/nexal-network\" service stop") {
		t.Errorf("a path with spaces was not quoted for the shell: %v", text)
	}
}

// withFastRepair drives the ladder without real waits or a real service: one
// login attempt per rung instead of a 30-second backoff, millisecond waits, and
// restart counted rather than performed. It returns the restart count and
// restores everything when the test ends.
func withFastRepair(t *testing.T, restart func(context.Context, *cobra.Command) error) *int {
	t.Helper()
	settle, wait, poll := daemonSettleAfterDown, daemonWaitAfterRestart, daemonWaitPoll
	backOff, restarter := loginWithBackOff, serviceRestarter

	count := 0
	daemonSettleAfterDown = time.Millisecond
	daemonWaitAfterRestart = 50 * time.Millisecond
	daemonWaitPoll = time.Millisecond
	loginWithBackOff = func(attempt func() error) error { return attempt() }
	serviceRestarter = func(ctx context.Context, cmd *cobra.Command) error {
		count++
		if restart != nil {
			return restart(ctx, cmd)
		}
		return nil
	}

	t.Cleanup(func() {
		daemonSettleAfterDown, daemonWaitAfterRestart, daemonWaitPoll = settle, wait, poll
		loginWithBackOff, serviceRestarter = backOff, restarter
	})
	return &count
}

// failingLogin fails its first `failures` attempts with the stuck-login error
// and succeeds after that.
func failingLogin(failures int) *fakeLogin { return &fakeLogin{failures: failures} }

type fakeLogin struct {
	calls    int
	failures int
}

func (f *fakeLogin) attempt() error {
	f.calls++
	if f.calls <= f.failures {
		return stuckLoginFailure
	}
	return nil
}

func quietCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(discard{})
	cmd.SetErr(discard{})
	return cmd
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// fakeDaemon answers the two calls the repair makes. statusFailures is how many
// times Status refuses before the daemon is back.
type fakeDaemon struct {
	downs          int
	statuses       int
	statusFailures int
}

func (f *fakeDaemon) Down(context.Context, *proto.DownRequest, ...grpc.CallOption) (*proto.DownResponse, error) {
	f.downs++
	return &proto.DownResponse{}, nil
}

func (f *fakeDaemon) Status(context.Context, *proto.StatusRequest, ...grpc.CallOption) (*proto.StatusResponse, error) {
	f.statuses++
	if f.statuses <= f.statusFailures {
		return nil, errors.New("connection refused")
	}
	return &proto.StatusResponse{}, nil
}
