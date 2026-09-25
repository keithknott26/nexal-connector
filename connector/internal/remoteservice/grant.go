// Package remoteservice validates coordinator-issued permission to request a
// temporary local service. Validation is deliberately separate from activation:
// a valid grant cannot turn on a macOS service without a signed privileged
// helper and an explicit administrator approval recorded by that helper.
package remoteservice

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Service string

const (
	SSH              Service = "ssh"
	VNC              Service = "vnc"
	Files            Service = "files"
	Wake             Service = "wake"
	maxGrantLifetime         = 24 * time.Hour
)

// Grant is signed by the coordinator and bound to both endpoints. Signature is
// unpadded base64url Ed25519 over SigningText(). IDs are opaque printable values;
// none may contain the separator used by the canonical representation.
type Grant struct {
	ID           string    `json:"id"`
	SourceHostID string    `json:"sourceHostId"`
	TargetHostID string    `json:"targetHostId"`
	Service      Service   `json:"service"`
	IssuedAt     time.Time `json:"issuedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Nonce        string    `json:"nonce"`
	Signature    string    `json:"signature"`
}

func (g Grant) SigningText() (string, error) {
	parts := []string{"nexal-remote-service-grant-v1", g.ID, g.SourceHostID, g.TargetHostID,
		string(g.Service), g.IssuedAt.UTC().Format(time.RFC3339Nano), g.ExpiresAt.UTC().Format(time.RFC3339Nano), g.Nonce}
	for _, value := range parts[1:] {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\n\r|") {
			return "", errors.New("invalid remote-service grant field")
		}
	}
	if !ValidService(g.Service) {
		return "", errors.New("unsupported remote service")
	}
	return strings.Join(parts, "|"), nil
}

func ValidService(s Service) bool { return s == SSH || s == VNC || s == Files || s == Wake }

// Verify rejects replay-to-another-host, premature, expired, and unreasonably
// long grants. acceptedSource is the authenticated peer asking for access;
// localHostID is this connector.
func Verify(g Grant, publicKey ed25519.PublicKey, acceptedSource, localHostID string, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("remote-service verifier is not configured")
	}
	if g.SourceHostID != acceptedSource || g.TargetHostID != localHostID {
		return errors.New("remote-service grant is bound to different computers")
	}
	if g.IssuedAt.After(now.Add(30*time.Second)) || !g.ExpiresAt.After(now) || !g.ExpiresAt.After(g.IssuedAt) || g.ExpiresAt.Sub(g.IssuedAt) > maxGrantLifetime {
		return errors.New("remote-service grant is outside its validity window")
	}
	text, err := g.SigningText()
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(g.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey, []byte(text), sig) {
		return errors.New("remote-service grant signature is invalid")
	}
	return nil
}

type Capability struct {
	Service               Service `json:"service"`
	ObservedEnabled       bool    `json:"observedEnabled"`
	TemporarilyAuthorized bool    `json:"temporarilyAuthorized"`
	RequiresAdminApproval bool    `json:"requiresAdminApproval"`
	ExpiresAt             string  `json:"expiresAt,omitempty"`
}

// Capabilities converts local observations and verified grants into an
// advertisement. Authorization never implies enabled: the helper must report
// the latter after macOS authorization and state verification.
func Capabilities(observed []string, grants []Grant, now time.Time) []Capability {
	on := map[Service]bool{}
	for _, name := range observed {
		switch name {
		case "ssh":
			on[SSH] = true
		case "vnc":
			on[VNC] = true
		case "smb":
			on[Files] = true
		}
	}
	out := make([]Capability, 0, 4)
	for _, service := range []Service{SSH, VNC, Files, Wake} {
		c := Capability{Service: service, ObservedEnabled: on[service], RequiresAdminApproval: service != Wake}
		for _, g := range grants {
			if g.Service == service && g.ExpiresAt.After(now) {
				c.TemporarilyAuthorized = true
				c.ExpiresAt = g.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
		out = append(out, c)
	}
	return out
}

func (s Service) Port() (uint16, error) {
	switch s {
	case SSH:
		return 22, nil
	case VNC:
		return 5900, nil
	case Files:
		return 445, nil
	}
	return 0, fmt.Errorf("%s is not a TCP service", s)
}
