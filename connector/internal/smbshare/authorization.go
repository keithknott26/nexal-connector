package smbshare

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// RecoveryAuthorization is coordinator-signed permission for one selected
// helper to serve one selected user's backup mount. It contains no credential.
// Signature is unpadded base64url Ed25519 over signingText().
type RecoveryAuthorization struct {
	GrantID      string    `json:"grantId"`
	HelperHostID string    `json:"helperHostId"`
	NetworkID    string    `json:"networkId"`
	AccountID    string    `json:"accountId"`
	UserID       string    `json:"userId"`
	Username     string    `json:"username"`
	ShareName    string    `json:"shareName"`
	MountPath    string    `json:"mountPath"`
	IssuedAt     time.Time `json:"issuedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Nonce        string    `json:"nonce"`
	Signature    string    `json:"signature"`
}

type RecoverySelection struct {
	HelperHostID string
	NetworkID    string
	AccountID    string
	Username     string
	ShareName    string
	MountPath    string
}

func (a RecoveryAuthorization) signingText() (string, error) {
	values := []string{"nexal-recovery-authorization-v1", a.GrantID, a.HelperHostID, a.NetworkID,
		a.AccountID, a.UserID, a.Username, a.ShareName, a.MountPath,
		a.IssuedAt.UTC().Format(time.RFC3339Nano), a.ExpiresAt.UTC().Format(time.RFC3339Nano), a.Nonce}
	for _, value := range values[1:] {
		if value == "" || len(value) > 1024 || strings.ContainsAny(value, "|\x00\r\n") {
			return "", errors.New("invalid recovery authorization field")
		}
	}
	if !filepath.IsAbs(a.MountPath) || filepath.Clean(a.MountPath) != a.MountPath || a.MountPath == "/" {
		return "", errors.New("recovery authorization mount must be an exact dedicated path")
	}
	return strings.Join(values, "|"), nil
}

// VerifyRecoveryAuthorization fails closed unless the coordinator selected this
// exact helper and exact backup mount. adminApproved is evidence supplied by the
// signed privileged-helper UI; a network grant never substitutes for local
// administrator consent.
func VerifyRecoveryAuthorization(a RecoveryAuthorization, key ed25519.PublicKey, selected RecoverySelection, now time.Time, adminApproved bool) error {
	if !adminApproved {
		return errors.New("explicit administrator authorization is required")
	}
	if len(key) != ed25519.PublicKeySize {
		return errors.New("recovery authorization verifier is not configured")
	}
	if a.HelperHostID != selected.HelperHostID || a.NetworkID != selected.NetworkID ||
		a.AccountID != selected.AccountID || a.Username != selected.Username ||
		a.ShareName != selected.ShareName || a.MountPath != selected.MountPath {
		return errors.New("recovery authorization does not match the selected helper, user, or backup mount")
	}
	if a.IssuedAt.After(now.Add(30*time.Second)) || !a.ExpiresAt.After(now) ||
		!a.ExpiresAt.After(a.IssuedAt) || a.ExpiresAt.Sub(a.IssuedAt) > MaxTimeBox {
		return errors.New("recovery authorization is outside its validity window")
	}
	text, err := a.signingText()
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(a.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, []byte(text), sig) {
		return errors.New("recovery authorization signature is invalid")
	}
	return nil
}

func DecodeRecoveryPublicKey(raw string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid pinned recovery authorization public key")
	}
	return ed25519.PublicKey(b), nil
}
