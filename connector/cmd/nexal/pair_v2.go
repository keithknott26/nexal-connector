package main

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/peeridentity"
	"nexal/connector/internal/qr"
)

// pairV2Command bootstraps an unenrolled Mac. It is additive while deployed
// clients migrate from the legacy, already-enrolled host pairing contract.
func pairV2Command(ctx context.Context, args []string) error {
	f, path, err := flags("pair-v2")
	if err != nil {
		return err
	}
	create := f.Bool("create", false, "create a version 2 enrollment session")
	statusID := f.String("status", "", "read a version 2 enrollment session")
	cancelID := f.String("cancel", "", "cancel a version 2 enrollment session")
	if err := parse(f, args, path); err != nil {
		return err
	}
	modes := 0
	if *create {
		modes++
	}
	if *statusID != "" {
		modes++
	}
	if *cancelID != "" {
		modes++
	}
	if modes != 1 {
		return errors.New("pair-v2 requires exactly one of --create, --status, or --cancel")
	}
	if *create {
		return createPairV2(ctx, *path)
	}
	if *cancelID != "" {
		return cancelPairV2(ctx, *path, *cancelID)
	}
	return statusPairV2(ctx, *path, *statusID)
}

func createPairV2(ctx context.Context, path string) error {
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		return err
	}
	witness := ""
	if cfg.Discovery != nil {
		witness = cfg.Discovery.DeviceFingerprint
	}
	id, _, err := peeridentity.EnsureIdentity(ctx, secrets, witness)
	if err != nil {
		return err
	}
	deviceID := peeridentity.Fingerprint(id)
	if cfg.Discovery == nil {
		cfg.Discovery = &config.Discovery{}
	}
	if cfg.Discovery.DeviceFingerprint == "" {
		cfg.Discovery.DeviceFingerprint = deviceID
		if err := config.Save(path, cfg); err != nil {
			return err
		}
	}
	api, err := client.New(cfg.Coordinator, "", cfg.Development)
	if err != nil {
		return err
	}
	session, err := api.CreateEnrollmentSession(ctx, client.EnrollmentSessionRequest{
		SchemaVersion: 2, DeviceName: cfg.Name, DeviceID: deviceID,
		PublicKey: base64.RawURLEncoding.EncodeToString(id.PublicKey), Platform: "darwin", Architecture: runtime.GOARCH,
	})
	if err != nil {
		return err
	}
	if err := secrets.Put(ctx, "enrollment-session-"+session.SessionID, session.PollToken); err != nil {
		return err
	}
	code, err := qr.Encode([]byte(session.UniversalLink))
	if err != nil {
		return err
	}
	return emit(map[string]any{"enrollment": map[string]any{
		"schemaVersion": 2, "sessionId": session.SessionID, "manualCode": session.ManualCode,
		"expiresAt": session.ExpiresAt, "status": session.Status, "step": "waiting_for_phone",
		"qr": map[string]any{"version": code.Version, "mask": code.Mask, "size": code.Size, "quietZone": qr.QuietZone, "errorLevel": "M", "encoding": "byte", "moduleRows": code.Rows()},
	}})
}

func statusPairV2(ctx context.Context, path, sessionID string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		return err
	}
	pollToken, err := secrets.Get(ctx, "enrollment-session-"+sessionID)
	if err != nil {
		return errors.New("enrollment session credential unavailable")
	}
	api, err := client.New(cfg.Coordinator, pollToken, cfg.Development)
	if err != nil {
		return err
	}
	state, err := api.EnrollmentSessionState(ctx, sessionID)
	if err != nil {
		return err
	}
	if state.Status == "paired" {
		if err := secrets.Put(ctx, "mesh-credential", state.Credential); err != nil {
			return err
		}
		if err := secrets.Put(ctx, "host", state.HostCredential); err != nil {
			return err
		}
		state.Credential = ""
		state.HostCredential = ""
		cfg.HostID = state.HostID
		cfg.Enrollment = &config.EnrollmentState{SchemaVersion: 2, Status: "paired", AccountID: state.AccountID, NetworkID: state.NetworkID, DeviceID: state.DeviceID, PairedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := config.Save(path, cfg); err != nil {
			return err
		}
	}
	return emit(map[string]any{"enrollmentStatus": state})
}

func cancelPairV2(ctx context.Context, path, sessionID string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "enrollment-session-"+sessionID)
	if err != nil {
		return errors.New("enrollment session credential unavailable")
	}
	api, err := client.New(cfg.Coordinator, token, cfg.Development)
	if err != nil {
		return err
	}
	if err := api.CancelEnrollmentSession(ctx, sessionID); err != nil {
		return err
	}
	return emit(map[string]any{"enrollmentCancelled": true, "sessionId": sessionID})
}
