package main

import (
	"context"
	"errors"
	"net"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/wol"
)

// codedError lets a command choose the stderr error code instead of the
// generic connector_error, so the native app can branch on a stable code
// rather than on message prose. Only wake uses it today.
type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

// errorCode is the stderr code for err: a codedError's code, else the historic
// connector_error, so every existing command's output is unchanged.
func errorCode(err error) string {
	var c *codedError
	if errors.As(err, &c) {
		return c.code
	}
	return "connector_error"
}

// wakeCommand wakes a sleeping Mac.
//
//	nexal wake --host <hostId>   ask the coordinator to have an awake neXal Mac
//	                             on that computer's LAN send the magic packet
//	nexal wake --mac <address>   send the magic packet from THIS Mac, now
//
// Wake-on-LAN is a LAN broadcast and cannot cross a router, which is why the
// --host form exists at all: the coordinator knows (from each Mac's reported
// lanKey) which awake Mac can reach the target and relays the request over the
// presence stream. --host authenticates with the enrolled host credential, like
// heartbeat; --mac touches no credential and no coordinator, so it works on an
// unenrolled Mac. Neither takes the configuration lock: both write nothing, and
// the menu-bar app calls this while `nexal run` holds that lock.
func wakeCommand(ctx context.Context, args []string) error {
	f, path, err := flags("wake")
	if err != nil {
		return err
	}
	host := f.String("host", "", "host id of the Mac to wake, relayed by the coordinator")
	mac := f.String("mac", "", "MAC address to wake directly from this Mac (aa:bb:cc:dd:ee:ff)")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if (*host == "") == (*mac == "") {
		return errors.New("usage: nexal wake --host <hostId> | --mac aa:bb:cc:dd:ee:ff")
	}
	if *mac != "" {
		hw, err := wol.ParseMAC(*mac)
		if err != nil {
			return err
		}
		n, err := wol.Send([]net.HardwareAddr{hw})
		if err != nil {
			return err
		}
		return emit(map[string]any{"sent": true, "interfaces": n})
	}
	if !client.ValidID(*host) {
		return errors.New("invalid host id")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(c.HostID) {
		return errors.New("enroll this host before requesting a wake")
	}
	if *host == c.HostID {
		return errors.New("this Mac is the one asking, so it is already awake")
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ack, err := api.RequestWake(ctx, *host)
	switch {
	case errors.Is(err, client.ErrNoWakeRelay):
		return &codedError{code: "no_wake_relay", err: err}
	case errors.Is(err, client.ErrWakeRateLimited):
		return &codedError{code: "rate_limited", err: err}
	case errors.Is(err, client.ErrWakeTargetNotFound):
		return &codedError{code: "wake_target_not_found", err: err}
	case client.IsNotSupported(err):
		return &codedError{code: "wake_unavailable",
			err: errors.New("remote wake is unavailable on the coordinator right now, or it does not support remote wake yet")}
	case err != nil:
		return err
	}
	// "requested" is the CLI's own statement that the coordinator accepted the
	// request for routing (it answers 202 with no such field); it is not a
	// claim that the target woke.
	return emit(map[string]any{"requested": true, "requestId": ack.RequestID, "relays": ack.Relays,
		"targetWakeForNetwork": ack.TargetWakeForNetwork})
}
