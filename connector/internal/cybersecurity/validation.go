package cybersecurity

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/google/uuid"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ValidationRun cannot carry shell commands, paths, scripts or arbitrary destinations.
type ValidationRun struct {
	ID        string    `json:"id"`
	Module    string    `json:"module"`
	ExpiresAt time.Time `json:"expiresAt"`
	Nonce     string    `json:"nonce"`
	TargetIP  string    `json:"targetIP"`
}

func (r ValidationRun) Validate(now time.Time) error {
	if id, err := uuid.Parse(r.ID); err != nil || id.String() != r.ID {
		return errors.New("invalid validation ID")
	}
	if id, err := uuid.Parse(r.Nonce); err != nil || id.String() != r.Nonce {
		return errors.New("invalid validation nonce")
	}
	if r.ExpiresAt.Sub(now) <= 0 || r.ExpiresAt.Sub(now) > 125*time.Second {
		return errors.New("invalid validation deadline")
	}
	switch r.Module {
	case "canary_integrity":
		return nil
	case "network_canary":
		ip, err := netip.ParseAddr(r.TargetIP)
		if err == nil && netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
			return nil
		}
	}
	return errors.New("unsupported validation module or destination")
}
func validationEvent(r ValidationRun) Event {
	return Event{SchemaVersion: 1, EventID: uuid.NewString(), ObservedAt: time.Now().UTC().Format(TimeLayout), Kind: "sensor_health", Severity: "info", Detector: "validation_" + r.Module, DetectorVersion: "1", OriginAssessment: "unknown", EvidenceRef: "validation_" + r.ID}
}

// ValidateLocal uses owned artifacts and the same canary implementation as normal detection.
// It does not infer a CVE verdict from an exercise match.
func (c Canary) ValidateLocal(ctx context.Context, r ValidationRun, report func(context.Context, Event) error) (bool, string, error) {
	if err := r.Validate(time.Now()); err != nil {
		return false, "execution_failed", err
	}
	if err := ctx.Err(); err != nil {
		return false, "cancelled", err
	}
	if r.Module != "canary_integrity" {
		return false, "execution_failed", errors.New("not a local exercise")
	}
	dir, err := os.MkdirTemp(c.Directory, "validation-")
	if err != nil {
		return false, "execution_failed", err
	}
	defer os.RemoveAll(dir)
	probe := Canary{Directory: dir}
	if err = probe.Configure(true); err != nil {
		return false, "execution_failed", err
	}
	if err = os.WriteFile(filepath.Join(dir, "security-canary", canaryName), []byte("NEXAL HARMLESS VALIDATION"), 0600); err != nil {
		return false, "execution_failed", err
	}
	err = probe.Tick(ctx, time.Now(), func(ctx context.Context, _ Event) error { return report(ctx, validationEvent(r)) })
	return true, "completed", err
}

// ServeValidation binds only the granted mesh IP for at most 90 seconds. A live
// coordinator check is required before accepting the one-use canary nonce.
func ServeValidation(ctx context.Context, r ValidationRun, authorized func(context.Context) bool, ready func(context.Context) error, report func(context.Context, Event) error) (bool, string, error) {
	if err := r.Validate(time.Now()); err != nil {
		return false, "execution_failed", err
	}
	if r.Module != "network_canary" {
		return false, "execution_failed", errors.New("not a network exercise")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(r.TargetIP, "48179"))
	if err != nil {
		return false, "sensor_unavailable", err
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	handler, result := validationHandler(ctx, r, authorized, report)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 2048, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	go server.Serve(listener)
	if err = ready(ctx); err != nil {
		return false, "cancelled", err
	}
	select {
	case err = <-result:
		return err == nil, "completed", err
	case <-ctx.Done():
		return false, "execution_failed", ctx.Err()
	}
}

func validationHandler(ctx context.Context, r ValidationRun, authorized func(context.Context) bool, report func(context.Context, Event) error) (http.Handler, <-chan error) {
	var mu sync.Mutex
	used := false
	result := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if used || req.Method != "POST" || req.URL.RequestURI() != "/validation/"+r.ID || req.ContentLength != 0 || subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+r.Nonce)) != 1 {
			http.Error(w, "not found", 404)
			return
		}
		if ctx.Err() != nil || !authorized(ctx) {
			http.Error(w, "expired", 410)
			return
		}
		used = true
		err := report(ctx, validationEvent(r))
		if err != nil {
			http.Error(w, "evidence unavailable", 503)
		} else {
			w.WriteHeader(204)
		}
		result <- err
	})
	return handler, result
}
