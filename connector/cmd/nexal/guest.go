package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
	"nexal/connector/internal/peeridentity"
)

type guestSession struct {
	SessionID  string `json:"sessionId"`
	CodeDigest string `json:"codeDigest"`
}

func guestCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("guest requires redeem, activate, status, install-guard, or guard")
	}
	action := args[0]
	f, path, err := flags("guest " + action)
	if err != nil {
		return err
	}
	stdin := f.Bool("code-stdin", false, "read invitation code from stdin")
	grant := f.String("grant-id", "", "guard grant")
	deadline := f.String("deadline", "", "guard deadline")
	received := f.String("received-at", "", "guard receipt")
	contact := f.String("contact", "", "verified inviter email")
	if err = parse(f, args[1:], path); err != nil {
		return err
	}
	switch action {
	case "guard":
		if os.Geteuid() != 0 {
			return errors.New("expiry guard requires root")
		}
		return runGuestGuard(ctx, *path, config.GuestAccess{GrantID: *grant, AccessExpiresAt: *deadline, ReceivedAt: *received, InviterEmail: *contact}, mesh.ExecRunner{})
	case "remove-guard":
		return removeGuestGuard(ctx)
	case "install-guard":
		return installGuestGuard(ctx, *path)
	case "status":
		return emitGuestStatus(*path)
	case "activate":
		return activateGuest(ctx, *path)
	case "redeem":
		if !*stdin {
			return errors.New("--code-stdin required; invitation codes must not appear in process arguments")
		}
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
		if err != nil || len(raw) >= 128 {
			return errors.New("invalid invitation code")
		}
		code, err := client.NormalizeGuestCode(string(raw))
		if err != nil {
			return err
		}
		return redeemGuest(ctx, *path, code)
	default:
		return errors.New("unknown guest action")
	}
}
func emitGuestStatus(path string) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	status := "none"
	if c.GuestAccess != nil {
		status = "active"
		if c.Enrollment == nil || c.Enrollment.Status != "paired" {
			status = "awaiting_guard"
		}
		if c.GuestAccess.IsExpired(time.Now()) {
			status = "expired"
		}
	}
	return emit(map[string]any{"status": status, "guestAccess": c.GuestAccess})
}
func redeemGuest(ctx context.Context, path, code string) error {
	unlock, err := lockStoppingAgent(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	if c.HostID != "" && (c.GuestAccess == nil || !c.GuestAccess.IsExpired(time.Now()) && (c.Enrollment == nil || c.Enrollment.Status != "joining")) {
		return errors.New("leave the current network before using another invitation")
	}

	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return err
	}
	if c.GuestAccess != nil && !c.GuestAccess.IsExpired(time.Now()) && c.Enrollment != nil && c.Enrollment.Status == "joining" {
		_, meshErr := secrets.Get(ctx, "mesh-credential")
		_, hostErr := secrets.Get(ctx, "host")
		if meshErr == nil && hostErr == nil {
			return emitGuestStatus(path)
		}
	}
	if c.GuestAccess != nil {
		request, stop := context.WithTimeout(ctx, 8*time.Second)
		err = (mesh.ExecRunner{}).Run(request, "nexal-network", "down")
		stop()
		if err != nil {
			return errors.New("could not disconnect expired access; retry before redeeming")
		}
	}
	witness := ""
	if c.Discovery != nil {
		witness = c.Discovery.DeviceFingerprint
	}
	identity, _, err := peeridentity.EnsureIdentity(ctx, secrets, witness)
	if err != nil {
		return err
	}
	deviceID := peeridentity.Fingerprint(identity)
	if c.Discovery == nil {
		c.Discovery = &config.Discovery{}
	}
	c.Discovery.DeviceFingerprint = deviceID
	if err = config.Save(path, c); err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, "", c.Development)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(code))
	codeDigest := hex.EncodeToString(sum[:])
	sessionPath := filepath.Join(filepath.Dir(path), "guest-session.json")
	var session guestSession
	if raw, e := config.ReadPrivate(sessionPath, 4096); e == nil {
		_ = json.Unmarshal(raw, &session)
	}
	if session.CodeDigest != codeDigest || session.SessionID == "" {
		minted, e := api.CreateEnrollmentSession(ctx, client.EnrollmentSessionRequest{SchemaVersion: 2, DeviceName: c.Name, DeviceID: deviceID, PublicKey: base64.RawURLEncoding.EncodeToString(identity.PublicKey), Platform: "darwin", Architecture: runtime.GOARCH})
		if e != nil {
			return e
		}
		if e = secrets.Put(ctx, "enrollment-session-"+minted.SessionID, minted.PollToken); e != nil {
			return e
		}
		session = guestSession{minted.SessionID, codeDigest}
		raw, _ := json.Marshal(session)
		if e = config.AtomicPrivate(sessionPath, raw); e != nil {
			return e
		}
	}
	pollToken, err := secrets.Get(ctx, "enrollment-session-"+session.SessionID)
	if err != nil {
		return errors.New("invitation session unavailable; request a new code")
	}
	started := time.Now()
	ctx, stop := context.WithTimeout(ctx, 40*time.Second)
	defer stop()
	for {
		requestStarted := time.Now()
		result, e := api.RedeemGuestInvitation(ctx, code, session.SessionID, pollToken)
		if e != nil {
			return e
		}
		serverNow, _ := time.Parse(time.RFC3339Nano, result.ServerNow)
		serverExpiry, _ := time.Parse(time.RFC3339Nano, result.AccessExpiresAt)
		g := &config.GuestAccess{GrantID: result.GrantID, MeshHostname: result.MeshHostname, ReceivedAt: requestStarted.UTC().Format(time.RFC3339Nano), AccessExpiresAt: requestStarted.Add(serverExpiry.Sub(serverNow)).UTC().Format(time.RFC3339Nano), InviterEmail: result.InviterEmail}
		if c.GuestAccess != nil && c.GuestAccess.GrantID == g.GrantID {
			previous, _ := time.Parse(time.RFC3339Nano, c.GuestAccess.AccessExpiresAt)
			candidate, _ := time.Parse(time.RFC3339Nano, g.AccessExpiresAt)
			if previous.Before(candidate) {
				g.AccessExpiresAt = c.GuestAccess.AccessExpiresAt
			}
			g.ReceivedAt = c.GuestAccess.ReceivedAt
		}
		if g.IsExpired(time.Now()) {
			return guestExpiredError(g)
		}
		if c.GuestAccess == nil || c.GuestAccess.GrantID != g.GrantID {
			if err = secrets.Delete(ctx, "host"); err != nil {
				return err
			}
			if err = secrets.Delete(ctx, "mesh-credential"); err != nil {
				return err
			}
			c.HostID = ""
			c.Enrollment = nil
		}
		c.GuestAccess = g
		c.Paused = true
		if err = config.Save(path, c); err != nil {
			return err
		}
		if result.Status == "joining" || result.Status == "paired" {
			if result.DeviceID != deviceID {
				return errors.New("invitation device identity mismatch")
			}
			if result.Credential == "" || result.HostCredential == "" {
				return errors.New("invitation credentials were already consumed; request a new code")
			}
			// Persist a restrictive deadline before any credential can start a tunnel.
			c.GuestAccess = g
			c.Paused = true
			c.HostID = result.HostID
			c.Enrollment = &config.EnrollmentState{SchemaVersion: 2, SessionID: session.SessionID, Status: "joining", AccountID: result.AccountID, NetworkID: result.NetworkID, DeviceID: result.DeviceID, ManagementURL: result.ManagementURL, PairedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err = config.Save(path, c); err != nil {
				return err
			}
			if err = secrets.Put(ctx, "mesh-credential", result.Credential); err != nil {
				return err
			}
			if err = secrets.Put(ctx, "host", result.HostCredential); err != nil {
				return err
			}
			// Guest persistence is now complete; ACK may be retried without joining.
			authAPI, e := client.New(c.Coordinator, pollToken, c.Development)
			if e != nil {
				return e
			}
			if e = authAPI.AcknowledgeEnrollmentCredentials(ctx, session.SessionID); e != nil {
				return e
			}
			return emitGuestStatus(path)
		}
		if time.Since(started) > 25*time.Second {
			return emit(map[string]any{"status": "provisioning", "guestAccess": g, "message": "Invitation accepted; enter the same code again shortly to finish provisioning."})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
func activateGuest(ctx context.Context, path string) error {
	unlock, err := lockStoppingAgent(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	if c.GuestAccess == nil {
		return errors.New("no temporary access grant")
	}
	if c.GuestAccess.IsExpired(time.Now()) {
		_ = expireGuest(ctx, path, &c)
		return guestExpiredError(c.GuestAccess)
	}
	if !guestGuardReady(ctx, path, *c.GuestAccess) {
		return errors.New("install the temporary-access expiry guard before joining")
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return err
	}
	if c.Enrollment == nil {
		return errors.New("temporary enrollment unavailable")
	}
	if !client.ValidGuestHostname(c.GuestAccess.GrantID, c.GuestAccess.MeshHostname) {
		return errors.New("temporary mesh identity unavailable")
	}
	if err = startGuestMesh(ctx, secrets, c.Enrollment.ManagementURL, *c.GuestAccess); err != nil {
		return err
	}
	c.Enrollment.Status = "paired"
	if err = config.Save(path, c); err != nil {
		return err
	}
	return emitGuestStatus(path)
}
func expireGuest(ctx context.Context, path string, c *config.Config) error {
	request, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	downErr := (mesh.ExecRunner{}).Run(request, "nexal-network", "down")
	if c.GuestAccess != nil {
		c.GuestAccess.Expired = true
	}
	c.Paused = true
	if c.Enrollment != nil {
		c.Enrollment.Status = "revoked"
	}
	saveErr := config.Save(path, *c)
	if secrets, err := config.NewSecrets(path, *c); err == nil {
		_ = secrets.Delete(request, "host")
		_ = secrets.Delete(request, "mesh-credential")
	}
	if downErr != nil {
		return errors.New("temporary access expired; local tunnel disconnect is still pending")
	}
	return saveErr
}

// Leave enough time for a bounded join to finish before the immutable lease.
func guestJoinDeadline(g config.GuestAccess, now time.Time) (time.Time, error) {
	deadline, err := time.Parse(time.RFC3339Nano, g.AccessExpiresAt)
	if err != nil || g.IsExpired(now) {
		return time.Time{}, guestExpiredError(&g)
	}
	if deadline.Sub(now) <= 35*time.Second {
		return time.Time{}, errors.New("too little temporary access remains to join safely; request a new invitation code")
	}
	return deadline.Add(-5 * time.Second), nil
}
func startGuestMesh(ctx context.Context, secrets config.Secrets, managementURL string, g config.GuestAccess) error {
	deadline, err := guestJoinDeadline(g, time.Now())
	if err != nil {
		return err
	}
	request, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err = startPersistedMesh(request, secrets, managementURL, g.MeshHostname)
	if err != nil || request.Err() != nil || g.IsExpired(time.Now()) {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		_ = (mesh.ExecRunner{}).Run(cleanup, "nexal-network", "down")
		if err != nil {
			return err
		}
		return guestExpiredError(&g)
	}
	return nil
}
