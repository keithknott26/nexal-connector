package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/qr"
)

// `nexal pair` is the MANUAL path for device pairing. The primary surface is the
// menu-bar app, which runs this same command rather than reimplementing any of it
// — there is one pairing implementation, one QR encoder and one payload
// validator in this system, and the UI is a view over them. That is why the
// command emits the module matrix in its JSON: if the Swift app encoded its own
// QR, the two surfaces could drift and only a phone would ever notice.
//
// OUTPUT SHAPE: stdout is JSON and only JSON, one object per line (the poll emits
// a line per status transition, so a single object would mean printing nothing
// until the end). The human-readable QR is drawn on STDERR, which keeps stdout
// machine-parseable and is also why the Mac app — which discards stderr — is
// unaffected by it.
//
// THE CLAIM TOKEN IS A SECRET. It authorizes a phone to claim this Mac. It is
// never printed as a field, never logged, never written to the configuration, and
// never placed in an error message. It exists in this process's memory and inside
// the rendered QR modules, which is unavoidable because the modules ARE the code
// the phone reads.

// pairPollInterval bounds how often the status endpoint is polled. The DEADLINE
// is not a constant: it comes from the coordinator's own expiresAt, because the
// server owns the pairing lifetime and a hardcoded five minutes here would go
// stale the moment that INSERT changes.
const (
	pairPollMinInterval = 2 * time.Second
	pairPollMaxInterval = 5 * time.Second
	// A small grace period past expiry so the final poll observes the
	// coordinator's own "expired" rather than this process inventing it.
	pairPollGrace = 3 * time.Second
)

func pairCommand(ctx context.Context, args []string) error {
	f, path, err := flags("pair")
	if err != nil {
		return err
	}
	role := f.String("role", "", "pairing role for this Mac: receiver or donor")
	cancel := f.String("cancel", "", "cancel the given pairing id instead of minting one")
	status := f.String("status", "", "report the current status of the given pairing id once and exit")
	ascii := f.Bool("ascii", false, "render the code with ASCII characters instead of Unicode half blocks")
	invert := f.Bool("invert", false, "swap ink and paper for a dark terminal background")
	noPoll := f.Bool("no-poll", false, "mint and render, then exit without waiting for the phone (the Mac app uses this and polls with --status)")
	if err := parse(f, args, path); err != nil {
		return err
	}
	// Exactly one mode. Silently preferring one over another would make a typo
	// look like a successful different operation.
	modes := 0
	for _, set := range []bool{*role != "", *cancel != "", *status != ""} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return errors.New("pair requires exactly one of --role receiver|donor, --status <pairingId> or --cancel <pairingId>")
	}
	api, device, err := pairClient(ctx, *path)
	if err != nil {
		return err
	}
	switch {
	case *cancel != "":
		if err := api.CancelPairing(ctx, device, *cancel); err != nil {
			return err
		}
		return emit(map[string]any{"pairingCancelled": true, "pairingId": *cancel,
			"note": "the coordinator cancels idempotently, so this is not evidence that a live pairing existed"})
	case *status != "":
		state, err := api.PairingState(ctx, device, *status)
		if err != nil {
			return err
		}
		return emitPairingStatus(state)
	}
	pairing, err := api.CreatePairing(ctx, device, *role)
	if err != nil {
		return err
	}
	payload, err := pairing.Payload()
	if err != nil {
		return err
	}
	code, err := qr.Encode(payload)
	if err != nil {
		return err
	}
	expiry, err := client.ParsePairingExpiry(pairing.ExpiresAt)
	if err != nil {
		return err
	}
	// The code goes to stderr; see the package comment. Written before the JSON so
	// a human watching a terminal sees the code immediately rather than after the
	// first poll.
	fmt.Fprint(os.Stderr, code.Terminal(qr.Style{ASCII: *ascii, Invert: *invert}))
	fmt.Fprintf(os.Stderr, "Scan with neXal@home as the %s device. Expires %s.\n",
		pairing.Role, expiry.UTC().Format(time.RFC3339))
	fmt.Fprintln(os.Stderr, "If the code looks like a photographic negative on this terminal, re-run with --invert.")
	if err := emit(map[string]any{"pairing": map[string]any{
		"pairingId":   pairing.PairingID,
		"role":        pairing.Role,
		"coordinator": pairing.Coordinator,
		"expiresAt":   pairing.ExpiresAt,
		"status":      client.PairingWaiting,
		"qr": map[string]any{
			"version":       code.Version,
			"mask":          code.Mask,
			"size":          code.Size,
			"quietZone":     qr.QuietZone,
			"errorLevel":    "M",
			"encoding":      "byte",
			"moduleRows":    code.Rows(),
			"moduleMeaning": "one string per row, '1' is a dark module, no quiet zone included",
		},
		"claimToken": "not emitted: the claim token exists only in this process's memory and inside the qr modules",
	}}); err != nil {
		return err
	}
	if *noPoll {
		return nil
	}
	return pollPairing(ctx, api, device, pairing.PairingID, expiry)
}

// pairClient loads the configuration and the enrolled host credential.
//
// It takes NO configuration lock, unlike enroll or static-peers: pairing writes
// nothing to the configuration file, and taking the exclusive lock would make
// `nexal pair` fail whenever the agent happens to hold it — a pairing attempt
// must not be blocked by a running connector, since a running connector is the
// normal state of a paired Mac.
func pairClient(ctx context.Context, path string) (*client.Client, string, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	if !client.ValidID(c.HostID) {
		return nil, "", errors.New("this Mac is not enrolled, so it has no host credential to pair with; run nexal enroll first")
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return nil, "", err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return nil, "", err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return nil, "", err
	}
	return api, c.HostID, nil
}

// pollPairing watches one pairing until it is scanned, cancelled, expired, the
// context is cancelled, or the coordinator's own expiry passes. Every transition
// is printed, so a surface (or a founder watching a terminal) sees what happened
// rather than only the outcome.
func pollPairing(ctx context.Context, api *client.Client, device, pairingID string, expiry time.Time) error {
	ctx, stop := context.WithDeadline(ctx, expiry.Add(pairPollGrace))
	defer stop()
	last := client.PairingWaiting
	for {
		interval := pairPollInterval(time.Until(expiry))
		select {
		case <-ctx.Done():
			// Not an error: a pairing nobody scanned is an ordinary outcome, and
			// exiting nonzero would make the Mac app report a failure for it.
			return emit(map[string]any{"pairingResult": map[string]any{
				"pairingId": pairingID, "status": client.PairingExpired, "scanned": false,
				"reason": "the pairing window closed before the coordinator reported a scan"}})
		case <-time.After(interval):
		}
		state, err := api.PairingState(ctx, device, pairingID)
		if err != nil {
			// A single failed poll must not discard a pairing that may still be
			// live, so transient failures are reported and the loop continues until
			// the deadline. The deadline is what bounds this, not a retry count.
			if ctx.Err() != nil {
				continue
			}
			if err := emit(map[string]any{"pairingPollError": err.Error(), "pairingId": pairingID}); err != nil {
				return err
			}
			continue
		}
		if state.Status != last {
			last = state.Status
			if err := emitPairingStatus(state); err != nil {
				return err
			}
		}
		if client.PairingTerminal(state.Status) {
			return emit(map[string]any{"pairingResult": map[string]any{
				"pairingId": pairingID, "status": state.Status,
				"scanned": state.Status == client.PairingScanned,
				"note":    "a scan means the phone claimed this pairing; it does not by itself link, share or enable any resource"}})
		}
	}
}

// pairPollInterval keeps the poll proportional to the time left: a leisurely
// interval while there are minutes to go, tightening as expiry approaches so the
// last transition is not missed by seconds.
func pairPollInterval(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return pairPollMinInterval
	}
	interval := remaining / 60
	if interval < pairPollMinInterval {
		return pairPollMinInterval
	}
	if interval > pairPollMaxInterval {
		return pairPollMaxInterval
	}
	return interval
}

func emitPairingStatus(state client.PairingStatus) error {
	return emit(map[string]any{"pairingStatus": map[string]any{
		"pairingId": state.PairingID, "status": state.Status, "expiresAt": state.ExpiresAt}})
}
