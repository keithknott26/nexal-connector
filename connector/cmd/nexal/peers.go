package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/peeridentity"
	"nexal/connector/internal/peerregistry"
	"nexal/connector/internal/pool"
)

// `nexal peers` is the owner's interface to peer membership: the record of which
// other machines this host will form a collective with.
//
// WHY THIS COMMAND EXISTS: pool.Registry is the authority NewRing consults, and
// it refuses any member it cannot find enrolled. Until membership could be
// created and persisted from outside a test, the collective could not involve a
// second machine however correct its transport was. internal/peerregistry made
// membership durable; this makes it reachable.
//
// THE HANDSHAKE IS DELIBERATELY TWO-SIDED AND OUT OF BAND. `invite` names the
// fingerprint it expects up front, `prove` can only be answered by the machine
// holding that private key, and `accept` verifies the signature over the
// invitation's random challenge. Possession of an address, a hostname, an mDNS
// record or a Thunderbolt cable authorizes nothing. The operator carries the
// invitation and the proof between machines, which is what makes the fingerprint
// verified out of band rather than asserted by whoever happens to answer.
func peersCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal peers invite|prove|accept|list|revoke [flags] [--config absolute-path]")
	}
	switch args[0] {
	case "invite":
		return peersInvite(args[1:])
	case "prove":
		return peersProve(ctx, args[1:])
	case "accept":
		return peersAccept(args[1:])
	case "list":
		return peersList(args[1:])
	case "revoke":
		return peersRevoke(args[1:])
	default:
		return fmt.Errorf("unknown peers subcommand %q", args[0])
	}
}

// openRegistry loads persisted membership under the configuration lock.
//
// The lock is taken before the read and held across the write by every mutating
// subcommand, because two `peers accept` runs racing would each load the same
// membership, add a different peer and write one result back, losing the other.
func openRegistry(path string) (*pool.Registry, string, func(), error) {
	unlock, err := config.Lock(path)
	if err != nil {
		return nil, "", nil, err
	}
	registryPath := peerregistry.Path(path)
	registry, err := peerregistry.Load(registryPath, nil)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	return registry, registryPath, unlock, nil
}

// selfIdentity loads this host's peer identity without minting one.
//
// `nexal identity` owns creation. A membership command that silently minted a key
// could hand out an invitation naming a fingerprint this host had only just
// invented, so an operator who had not yet run `identity` is told to, rather than
// given a second identity from a second code path.
func selfIdentity(ctx context.Context, path string) (pool.Identity, error) {
	c, err := config.Load(path)
	if err != nil {
		return pool.Identity{}, err
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return pool.Identity{}, err
	}
	id, err := peeridentity.LoadIdentity(ctx, secrets)
	if err != nil {
		return pool.Identity{}, fmt.Errorf("%w; run `nexal identity` on this Mac first", err)
	}
	return id, nil
}

// readBoundedStdin reads at most limit bytes. An invitation or proof is a few
// hundred bytes; anything larger is a mistake or an attempt to exhaust memory,
// so it is refused rather than truncated -- a truncated proof would fail
// signature verification with a confusing error instead of an honest one.
func readBoundedStdin(limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return b, nil
}

// peersInvite mints a one-use invitation for a named fingerprint.
//
// The fingerprint is required and is not discoverable by this command on
// purpose: the owner must have obtained it from the other machine's `nexal
// identity` output through a channel they trust. An invitation that let the
// far end choose which identity it was for would authorize whoever answered.
func peersInvite(args []string) error {
	f, path, err := flags("peers invite")
	if err != nil {
		return err
	}
	fingerprint := f.String("fingerprint", "", "the peer's 64-hex device fingerprint from `nexal identity`")
	// Ten minutes is pool.Registry's own ceiling, matching `nexal enroll`.
	ttl := f.Duration("ttl", 10*time.Minute, "how long the invitation stays valid, at most 10m")
	contributor := f.Bool("contributor", true, "peer may contribute resources to collectives")
	admin := f.Bool("administrator", false, "peer may administer this pool")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 || *fingerprint == "" {
		return errors.New("usage: nexal peers invite --fingerprint 64-hex [--ttl 10m] [--administrator] [--config absolute-path]")
	}
	var roles []pool.Role
	if *contributor {
		roles = append(roles, pool.Contributor)
	}
	if *admin {
		roles = append(roles, pool.Administrator)
	}
	if len(roles) == 0 {
		return errors.New("an invitation must grant at least one role")
	}

	registry, registryPath, unlock, err := openRegistry(*path)
	if err != nil {
		return err
	}
	defer unlock()
	invitation, err := registry.Invite(*fingerprint, roles, *ttl)
	if err != nil {
		return err
	}
	// The invitation is persisted only as far as the membership it will produce:
	// pending invitations are intentionally not written to disk, because they are
	// one-use and short-lived and a restart should let them lapse. Saving here
	// keeps the file consistent with any expiry sweep Invite performed.
	if err := peerregistry.Save(registryPath, registry); err != nil {
		return err
	}
	return emit(map[string]any{
		"invitation": invitation,
		"next":       "run `nexal peers prove` on the invited Mac with this invitation, then `nexal peers accept` back here with its proof",
		"warning":    "carry this over a channel you trust; it authorizes the named fingerprint only",
	})
}

// peersProve answers an invitation on the invited machine.
//
// Prove refuses an invitation naming a different fingerprint, so an invitation
// intercepted in transit cannot be answered by the machine that intercepted it.
func peersProve(ctx context.Context, args []string) error {
	f, path, err := flags("peers prove")
	if err != nil {
		return err
	}
	invitationJSON := f.String("invitation", "", "the invitation JSON from `nexal peers invite`, or - to read stdin")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 || *invitationJSON == "" {
		return errors.New("usage: nexal peers prove --invitation json|- [--config absolute-path]")
	}
	raw := []byte(*invitationJSON)
	if *invitationJSON == "-" {
		// Bounded: an invitation is a few hundred bytes, so a large read is a
		// mistake or an attack, not an invitation.
		if raw, err = readBoundedStdin(64 << 10); err != nil {
			return err
		}
	}
	var invitation pool.Invitation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&invitation); err != nil {
		return fmt.Errorf("invitation is not valid: %w", err)
	}

	id, err := selfIdentity(ctx, *path)
	if err != nil {
		return err
	}
	proof, err := id.Prove(invitation)
	if err != nil {
		// Prove's only failure that matters to an operator is the mismatch, so
		// name it rather than passing ErrUnauthorized through bare.
		return fmt.Errorf("%w: this invitation names fingerprint %s, which is not this Mac (%s)",
			err, invitation.ExpectedDeviceID, pool.DeviceID(id.PublicKey))
	}
	return emit(map[string]any{
		"proof": map[string]any{
			"invitationId": proof.InvitationID,
			"publicKey":    base64.StdEncoding.EncodeToString(proof.PublicKey),
			"signature":    base64.StdEncoding.EncodeToString(proof.Signature),
		},
		"next": "run `nexal peers accept` on the inviting Mac with this proof",
	})
}

// wireProof is the transported form. ed25519 keys and signatures are base64 in
// JSON rather than Go's default byte-array encoding so an operator can paste
// them between machines without the shape changing.
type wireProof struct {
	InvitationID string `json:"invitationId"`
	PublicKey    string `json:"publicKey"`
	Signature    string `json:"signature"`
}

// peersAccept completes enrollment on the inviting machine and persists it.
func peersAccept(args []string) error {
	f, path, err := flags("peers accept")
	if err != nil {
		return err
	}
	proofJSON := f.String("proof", "", "the proof JSON from `nexal peers prove`, or - to read stdin")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 || *proofJSON == "" {
		return errors.New("usage: nexal peers accept --proof json|- [--config absolute-path]")
	}
	raw := []byte(*proofJSON)
	if *proofJSON == "-" {
		if raw, err = readBoundedStdin(64 << 10); err != nil {
			return err
		}
	}
	var wire wireProof
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("proof is not valid: %w", err)
	}
	publicKey, err := base64.StdEncoding.DecodeString(wire.PublicKey)
	if err != nil {
		return errors.New("proof publicKey is not valid base64")
	}
	signature, err := base64.StdEncoding.DecodeString(wire.Signature)
	if err != nil {
		return errors.New("proof signature is not valid base64")
	}

	registry, registryPath, unlock, err := openRegistry(*path)
	if err != nil {
		return err
	}
	defer unlock()
	// Enroll verifies the signature over this invitation's random challenge and
	// that the key hashes to the fingerprint the invitation named. Neither this
	// command nor the operator can wave that through.
	member, err := registry.Enroll(pool.EnrollmentProof{
		InvitationID: wire.InvitationID, PublicKey: publicKey, Signature: signature,
	})
	if err != nil {
		return err
	}
	if err := peerregistry.Save(registryPath, registry); err != nil {
		return err
	}
	return emit(map[string]any{
		"enrolled": map[string]any{
			"deviceId":   member.DeviceID,
			"roles":      member.Roles,
			"enrolledAt": member.EnrolledAt,
		},
		"note": "membership is persisted and survives a restart; add the peer's endpoint with `nexal static-peers add` before running a collective",
	})
}

func peersList(args []string) error {
	f, path, err := flags("peers list")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal peers list [--config absolute-path]")
	}
	// Read-only, but still locked: a concurrent accept would otherwise be
	// observed half-written.
	registry, _, unlock, err := openRegistry(*path)
	if err != nil {
		return err
	}
	defer unlock()
	members := registry.Snapshot()
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		row := map[string]any{"deviceId": m.DeviceID, "roles": m.Roles, "enrolledAt": m.EnrolledAt}
		// Revoked members are listed, not hidden: the owner needs to see that a
		// machine was removed rather than find it simply absent.
		if m.RevokedAt != nil {
			row["revokedAt"] = *m.RevokedAt
		}
		out = append(out, row)
	}
	return emit(map[string]any{"members": out, "count": len(out)})
}

// peersRevoke removes a peer's authority. The record is kept, marked revoked, so
// the machine cannot simply enroll again as though it were new.
func peersRevoke(args []string) error {
	f, path, err := flags("peers revoke")
	if err != nil {
		return err
	}
	fingerprint := f.String("fingerprint", "", "the peer's 64-hex device fingerprint")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 || *fingerprint == "" {
		return errors.New("usage: nexal peers revoke --fingerprint 64-hex [--config absolute-path]")
	}
	registry, registryPath, unlock, err := openRegistry(*path)
	if err != nil {
		return err
	}
	defer unlock()
	if err := registry.Revoke(*fingerprint); err != nil {
		return err
	}
	if err := peerregistry.Save(registryPath, registry); err != nil {
		return err
	}
	return emit(map[string]any{
		"revoked": *fingerprint,
		"note":    "the ring re-checks revocation per request, so an in-flight collective stops using this peer",
	})
}
