package main

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
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
	resetLocal := f.Bool("reset-local", false, "clear an unfinished local enrollment")
	leave := f.Bool("leave", false, "leave the current neXal network")
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
	if *resetLocal {
		modes++
	}
	if *leave {
		modes++
	}
	if modes != 1 {
		return errors.New("pair-v2 requires exactly one of --create, --status, --cancel, --reset-local, or --leave")
	}
	if *create {
		return createPairV2(ctx, *path)
	}
	if *cancelID != "" {
		return cancelPairV2(ctx, *path, *cancelID)
	}
	if *resetLocal {
		return resetLocalPairV2(ctx, *path)
	}
	if *leave {
		return leavePairV2(ctx, *path)
	}
	return statusPairV2(ctx, *path, *statusID)
}

func leavePairV2(ctx context.Context, path string) error {
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Enrollment == nil || cfg.HostID == "" {
		return emit(map[string]any{"left": true})
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		return err
	}
	hostToken, err := secrets.Get(ctx, "host")
	if err != nil {
		return errors.New("host credential unavailable; remove this computer from the iPhone app")
	}
	api, err := client.New(cfg.Coordinator, hostToken, cfg.Development)
	if err != nil {
		return err
	}
	if err := api.LeaveMeshNetwork(ctx); err != nil {
		return err
	}
	// Server revocation is authoritative. Stopping the local runtime is best
	// effort because an already-revoked machine must still become locally reset.
	_ = (mesh.ExecRunner{}).Run(ctx, "nexal-network", "down")
	for _, name := range []string{"host", "mesh-credential", "enrollment-session-" + cfg.Enrollment.SessionID} {
		if err := secrets.Delete(ctx, name); err != nil {
			return err
		}
	}
	cfg.HostID, cfg.Enrollment, cfg.Tunnel = "", nil, nil
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	return emit(map[string]any{"left": true})
}

func resetLocalPairV2(ctx context.Context, path string) error {
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Enrollment == nil {
		return emit(map[string]any{"localEnrollmentReset": true})
	}
	if cfg.Enrollment.Status == "paired" {
		return errors.New("a paired Mac must be removed from the iPhone app before resetting it locally")
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		return err
	}
	for _, name := range []string{"host", "mesh-credential", "enrollment-session-" + cfg.Enrollment.SessionID} {
		if err := secrets.Delete(ctx, name); err != nil {
			return err
		}
	}
	cfg.HostID = ""
	cfg.Enrollment = nil
	cfg.Tunnel = nil
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	return emit(map[string]any{"localEnrollmentReset": true})
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
	hasCredentials := state.Credential != "" || state.HostCredential != ""
	if (state.Status == "joining" || state.Status == "paired") && hasCredentials {
		if err := secrets.Put(ctx, "mesh-credential", state.Credential); err != nil {
			return err
		}
		if err := secrets.Put(ctx, "host", state.HostCredential); err != nil {
			return err
		}
		state.Credential = ""
		state.HostCredential = ""
		cfg.HostID = state.HostID
		cfg.Enrollment = &config.EnrollmentState{SchemaVersion: 2, SessionID: sessionID, Status: state.Status, AccountID: state.AccountID, NetworkID: state.NetworkID, DeviceID: state.DeviceID, ManagementURL: state.ManagementURL, PairedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpiresAt: state.ExpiresAt}
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		if err := acknowledgePersistedCredentials(ctx, api, sessionID); err != nil {
			return err
		}
		if err := startPersistedMesh(ctx, secrets, cfg.Enrollment.ManagementURL); err != nil {
			return err
		}
	} else if state.Status == "joining" || state.Status == "paired" {
		if !persistedEnrollment(cfg, sessionID) {
			return errors.New("enrollment credentials are unavailable before durable local storage")
		}
		cfg.Enrollment.Status = state.Status
		cfg.Enrollment.ManagementURL = state.ManagementURL
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		if err := startPersistedMesh(ctx, secrets, cfg.Enrollment.ManagementURL); err != nil {
			return err
		}
	}
	return emit(map[string]any{"enrollmentStatus": state})
}

func startPersistedMesh(ctx context.Context, secrets config.Secrets, managementURL string) error {
	setupKey, err := secrets.Get(ctx, "mesh-credential")
	if err != nil {
		return errors.New("secure networking credential unavailable")
	}
	controller := mesh.Controller{
		Plans:  mesh.StaticPlanStore{Plan: mesh.StartupPlan{SetupKey: setupKey, ManagementURL: managementURL}},
		Runner: mesh.ExecRunner{},
	}
	return controller.Start(ctx)
}

func persistedEnrollment(cfg config.Config, sessionID string) bool {
	return cfg.Enrollment != nil && (cfg.Enrollment.Status == "joining" || cfg.Enrollment.Status == "paired") && cfg.Enrollment.SessionID == sessionID && cfg.HostID != ""
}

type enrollmentAcknowledger interface {
	AcknowledgeEnrollmentCredentials(context.Context, string) error
}

func acknowledgePersistedCredentials(ctx context.Context, api enrollmentAcknowledger, sessionID string) error {
	// The platform ACK is idempotent. Never reinterpret authentication or state
	// errors as success: a lost response is recovered by retrying the same ACK.
	return api.AcknowledgeEnrollmentCredentials(ctx, sessionID)
}

func enrollmentStatusFromConfig(cfg config.Config, sessionID string) client.EnrollmentSessionStatus {
	e := cfg.Enrollment
	return client.EnrollmentSessionStatus{SchemaVersion: 2, SessionID: sessionID, Status: "paired", Step: "paired",
		AccountID: e.AccountID, NetworkID: e.NetworkID, DeviceID: e.DeviceID, HostID: cfg.HostID, ManagementURL: e.ManagementURL, ExpiresAt: e.ExpiresAt}
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
