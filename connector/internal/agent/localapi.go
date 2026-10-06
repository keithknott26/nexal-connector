package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"nexal/connector/internal/config"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func (a *Agent) Handler(adminToken string) (http.Handler, error) {
	if len(adminToken) < 32 || len(adminToken) > 4096 {
		return nil, errors.New("strong local admin token required")
	}
	expected := sha256.Sum256([]byte("Bearer " + adminToken))
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
			apiError(w, 403, "loopback_required")
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || r.Header.Get("Origin") != "" {
			apiError(w, 403, "local_nonbrowser_client_required")
			return
		}
		sum := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(expected[:], sum[:]) != 1 {
			apiError(w, 401, "unauthorized")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			apiError(w, 429, "request_limit")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.URL.RawQuery != "" {
			apiError(w, 400, "query_not_allowed")
			return
		}
		if r.URL.Path == "/v1/policy" {
			switch r.Method {
			case http.MethodGet:
				if r.ContentLength != 0 {
					apiError(w, 400, "body_not_allowed")
					return
				}
				writeJSON(w, 200, a.Snapshot().ResourcePolicy)
			case http.MethodPut:
				mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mediaType != "application/json" {
					apiError(w, 415, "json_required")
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					apiError(w, 413, "body_too_large")
					return
				}
				policy, err := config.DecodeResourcePolicy(body)
				if err != nil {
					apiError(w, 400, "invalid_resource_policy")
					return
				}
				if err = a.SetResourcePolicy(policy); err != nil {
					apiError(w, 500, "policy_persistence_failed_host_paused")
					return
				}
				writeJSON(w, 200, a.Snapshot().ResourcePolicy)
			default:
				w.Header().Set("Allow", "GET, PUT")
				apiError(w, 405, "method_not_allowed")
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/attempts") || strings.HasPrefix(r.URL.Path, "/v1/jobs") {
			// Presence of an Access header or a client-provided grant is not
			// authentication. Reject ALL incoming job requests until signature,
			// issuer/audience, replay and independent deployment verification
			// are implemented. The local admin secret never bypasses this.
			apiError(w, 403, "verified_tunnel_dispatch_not_integrated")
			return
		}
		if r.URL.Path == "/v1/status" && r.Method == "GET" {
			if r.ContentLength != 0 {
				apiError(w, 400, "body_not_allowed")
				return
			}
			writeJSON(w, 200, a.Snapshot())
			return
		}
		// Read-only, like /v1/status: the Mac app polls it to list the other Macs.
		if r.URL.Path == "/v1/peers" && r.Method == "GET" {
			if r.ContentLength != 0 {
				apiError(w, 400, "body_not_allowed")
				return
			}
			writeJSON(w, 200, a.PeersSnapshot())
			return
		}
		if r.Method != "POST" {
			apiError(w, 405, "method_not_allowed")
			return
		}
		// Commands take no parameters. Empty body or exactly {} are accepted.
		b, err := io.ReadAll(r.Body)
		if err != nil {
			apiError(w, 413, "body_too_large")
			return
		}
		trim := strings.TrimSpace(string(b))
		if trim != "" && trim != "{}" {
			apiError(w, 400, "invalid_command_body")
			return
		}
		switch r.URL.Path {
		case "/v1/accept-jobs":
			err = a.AcceptJobsNow()
			if err != nil {
				apiError(w, 409, "manual_acceptance_unavailable")
				return
			}
		case "/v1/pause":
			err = a.SetPaused(true)
		case "/v1/resume":
			err = a.SetPaused(false)
		case "/v1/cancel":
			a.Cancel()
		case "/v1/share-while-active-on":
			err = a.SetShareWhileActive(true)
		case "/v1/share-while-active-off":
			err = a.SetShareWhileActive(false)
		default:
			apiError(w, 404, "not_found")
			return
		}
		if err != nil {
			apiError(w, 500, "policy_persistence_failed")
			return
		}
		writeJSON(w, 200, a.Snapshot())
	}), nil
}

func (a *Agent) Serve(ctx context.Context, adminToken string) error {
	a.mu.Lock()
	listen := a.cfg.Listen
	a.mu.Unlock()
	if err := config.ValidateListen(listen); err != nil {
		return err
	}
	handler, err := a.Handler(adminToken)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return errors.New("local API unavailable; another connector may be running")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := server.Shutdown(stopCtx); err != nil {
				// A refused graceful shutdown means a handler is still running
				// past the 3 s budget; the process exits regardless.
				a.logger.Warn("local API shutdown incomplete", "error", errorText(err))
			}
		case <-stopped:
		}
	}()
	err = server.Serve(ln)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		// The returned error is fixed so nothing about the listener leaks to the
		// caller; the log keeps the sanitized reason for the operator.
		a.logger.Error("local API stopped unexpectedly", "error", errorText(err))
		return errors.New("local API stopped unexpectedly")
	}
	return nil
}
