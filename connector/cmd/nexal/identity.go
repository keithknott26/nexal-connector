package main

import (
	"context"
	"errors"

	"nexal/connector/internal/config"
	"nexal/connector/internal/peeridentity"
)

// `nexal identity` prints this host's peer fingerprint, generating the
// underlying key on first run.
//
// WHY THIS COMMAND EXISTS: `static-peers add --fingerprint` requires the peer's
// 64-hex device ID, and until now nothing in the connector could tell an
// operator what their own machine's fingerprint was. pool.NewIdentity was never
// called outside tests, so no key existed to derive one from, which made linking
// two Macs impossible to complete by hand however correct the transport was.
//
// OUTPUT IS STDOUT JSON, matching every other command, so the menu-bar app can
// consume it instead of reimplementing key handling. `created` distinguishes the
// first run from later ones: the operator needs to know whether the value they
// are looking at is new (and must be carried to the other machine) or the same
// one their peers already pinned.
//
// THE PRIVATE KEY IS NEVER PRINTED, not even redacted, and no flag exposes it.
// The only thing this command reveals is a hash of the public half, which peers
// must know by design.
func identityCommand(ctx context.Context, args []string) error {
	f, path, err := flags("identity")
	if err != nil {
		return err
	}
	// Deliberately not a mutating flag: rotation changes this host's identity and
	// breaks every peer that pinned the old fingerprint, so it is not reachable
	// from the command whose job is "make sure I have one". A future `identity
	// rotate` can own that, with its own confirmation.
	show := f.Bool("fingerprint-only", false, "print only the fingerprint, for scripts")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal identity [--fingerprint-only] [--config absolute-path]")
	}

	// The exclusive lock is taken BEFORE reading, and held across the key write
	// and the config write, because this command can create both. Two connectors
	// running `identity` concurrently on first boot would otherwise each mint a
	// key, and the one that lost the race would be left publishing a fingerprint
	// whose private half was discarded.
	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()

	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	// Pass the configured fingerprint as the witness that an identity already
	// exists, so a Keychain that is merely unavailable can never be mistaken for
	// a first run and silently rotate this host's identity.
	var configured string
	if c.Discovery != nil {
		configured = c.Discovery.DeviceFingerprint
	}
	id, created, err := peeridentity.EnsureIdentity(ctx, secrets, configured)
	if err != nil {
		return err
	}
	fingerprint := peeridentity.Fingerprint(id)
	if fingerprint == "" {
		return errors.New("peer identity did not yield a device fingerprint")
	}

	// Persist the fingerprint into the config so discovery can publish it and so
	// the value survives for anyone reading the file directly. Only written when
	// it differs, which keeps a read-only `identity` call from rewriting the file
	// on every invocation.
	//
	// A DISAGREEMENT IS AN ERROR, NOT A CORRECTION. If the config already names a
	// different fingerprint, the stored key and the published identity have
	// diverged -- most likely a config restored from another machine's backup, or
	// a key removed from the Keychain while the config survived. Silently
	// rewriting it would make this host claim an identity its peers never pinned,
	// so the operator has to resolve it.
	switch {
	case c.Discovery == nil:
		c.Discovery = &config.Discovery{DeviceFingerprint: fingerprint}
		if err := config.Save(*path, c); err != nil {
			return err
		}
	case c.Discovery.DeviceFingerprint == "":
		c.Discovery.DeviceFingerprint = fingerprint
		if err := config.Save(*path, c); err != nil {
			return err
		}
	case c.Discovery.DeviceFingerprint != fingerprint:
		// Unreachable: EnsureIdentity already rejects a configured fingerprint
		// that disagrees with the stored key. Kept as a belt-and-braces guard so
		// a future change there cannot quietly make this command rewrite the
		// fingerprint peers have pinned.
		return errors.New("configured deviceFingerprint does not match the stored peer key")
	}

	if *show {
		return emit(fingerprint)
	}
	return emit(map[string]any{
		"deviceFingerprint": fingerprint,
		"created":           created,
		"peerPort":          c.Discovery.PeerPort,
		"privateKey":        "stored in the connector secrets store; never printed and never written to the configuration",
		"nextStep": "give this fingerprint to the OTHER machine: " +
			"nexal static-peers add --endpoint https://<this-host-private-ip>:<peerPort> --fingerprint " + fingerprint,
		"authorization": "a fingerprint identifies a device; it does not authorize it. " +
			"The coordinator's authorized set still decides whether a dial is permitted.",
	})
}
