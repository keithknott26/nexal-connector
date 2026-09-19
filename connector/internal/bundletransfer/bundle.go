// Package bundletransfer moves only disposable pager credentials, never arbitrary files.
// It is opt-in, separate from execution and the public marketplace.
package bundletransfer

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

const MaxPlaintext = 6000

var Names = []string{"ca.pem", "cert.pem", "key.pem", "connection.json"}

type Bundle map[string][]byte

func Fingerprint(public []byte) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:])
}
func aad(t client.Transfer) ([]byte, error) {
	if !client.ValidID(t.ID) || !client.ValidID(t.DonorHostID) || !client.ValidID(t.ReceiverHostID) ||
		t.DonorHostID == t.ReceiverHostID {
		return nil, errors.New("invalid transfer binding")
	}
	expiry, err := time.Parse(time.RFC3339Nano, t.ExpiresAt)
	if err != nil || !expiry.After(time.Now()) || expiry.After(time.Now().Add(11*time.Minute)) {
		return nil, errors.New("invalid transfer expiry")
	}
	return []byte("nexal-pager-bundle-v1\n" + t.ID + "\n" + t.DonorHostID + "\n" + t.ReceiverHostID + "\n" + t.PublicKey + "\n" + t.ExpiresAt), nil
}
func aead(secret, binding []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, string(binding), 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func Seal(t client.Transfer, expectedFingerprint string, b Bundle) (string, error) {
	binding, err := aad(t)
	if err != nil {
		return "", err
	}
	public, err := base64.StdEncoding.Strict().DecodeString(t.PublicKey)
	if err != nil || len(expectedFingerprint) != 64 || Fingerprint(public) != expectedFingerprint {
		return "", errors.New("receiver public-key fingerprint mismatch")
	}
	pk, err := mlkem.NewEncapsulationKey768(public)
	if err != nil {
		return "", errors.New("invalid recipient encryption key")
	}
	plain, err := json.Marshal(b)
	if err != nil || len(plain) > MaxPlaintext {
		return "", errors.New("bundle exceeds transfer bound")
	}
	defer clear(plain)
	if err := Validate(b); err != nil {
		return "", err
	}
	secret, kem := pk.Encapsulate()
	defer clear(secret)
	gcm, err := aead(secret, binding)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	wire := append(append(kem, nonce...), gcm.Seal(nil, nonce, plain, binding)...)
	return base64.StdEncoding.EncodeToString(wire), nil
}
func Open(t client.Transfer, key *mlkem.DecapsulationKey768) (Bundle, error) {
	binding, err := aad(t)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(key.EncapsulationKey().Bytes()) != t.PublicKey || t.Ciphertext == nil {
		return nil, errors.New("transfer is not bound to this receiver")
	}
	if len(*t.Ciphertext) > 12000 {
		return nil, errors.New("ciphertext exceeds bound")
	}
	wire, err := base64.StdEncoding.Strict().DecodeString(*t.Ciphertext)
	const kemSize = mlkem.CiphertextSize768
	if err != nil || len(wire) < kemSize+12+16 || len(wire) > kemSize+12+16+MaxPlaintext {
		return nil, errors.New("invalid encrypted bundle")
	}
	secret, err := key.Decapsulate(wire[:kemSize])
	if err != nil {
		return nil, errors.New("cannot decapsulate transfer")
	}
	defer clear(secret)
	gcm, err := aead(secret, binding)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, wire[kemSize:kemSize+12], wire[kemSize+12:], binding)
	if err != nil {
		return nil, errors.New("encrypted bundle authentication failed")
	}
	defer clear(plain)
	if err := config.CheckJSONObject(plain); err != nil {
		return nil, errors.New("ambiguous bundle JSON")
	}
	var b Bundle
	if json.Unmarshal(plain, &b) != nil {
		return nil, errors.New("invalid bundle JSON")
	}
	if err := Validate(b); err != nil {
		return nil, err
	}
	return b, nil
}
func Validate(b Bundle) error {
	bad := errors.New("invalid, expired or unexpected pager credentials")
	if len(b) != 4 {
		return bad
	}
	for _, name := range Names {
		if len(b[name]) == 0 || len(b[name]) > MaxPlaintext {
			return bad
		}
	}
	if config.CheckJSONObject(b["connection.json"]) != nil {
		return bad
	}
	var m struct {
		Schema   int       `json:"schemaVersion"`
		Endpoint string    `json:"endpoint"`
		Pages    int       `json:"pages"`
		CA       string    `json:"caSHA256"`
		Expires  time.Time `json:"expiresAt"`
	}
	d := json.NewDecoder(bytes.NewReader(b["connection.json"]))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF ||
		m.Schema != 1 || m.Pages != 64 || !m.Expires.After(time.Now()) || Fingerprint(b["ca.pem"]) != m.CA {
		return bad
	}
	host, port, err := net.SplitHostPort(m.Endpoint)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsPrivate() || ip.IsLoopback() {
		return bad
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return bad
		}
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil || p < 1 || p > 65535 {
		return bad
	}
	block, rest := pem.Decode(b["ca.pem"])
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return bad
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || m.Expires.After(ca.NotAfter) || time.Now().Before(ca.NotBefore) {
		return bad
	}
	pair, err := tls.X509KeyPair(b["cert.pem"], b["key.pem"])
	if err != nil || len(pair.Certificate) != 1 {
		return bad
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return bad
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return bad
	}
	return nil
}
func Read(dir string) (Bundle, error) {
	st, err := os.Lstat(dir)
	if err != nil || !filepath.IsAbs(dir) || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("bundle must be an absolute private directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		return nil, errors.New("select only the donor's four-file client folder")
	}
	b := Bundle{}
	for _, name := range Names {
		content, err := config.ReadPrivate(filepath.Join(dir, name), MaxPlaintext)
		if err != nil {
			return nil, errors.New("cannot safely read pager client file")
		}
		b[name] = content
	}
	return b, Validate(b)
}

// Install uses a fresh private directory, never overwrites Downloads or accepts
// remote pathnames. The parent must already be trusted and private.
func Install(parent string, b Bundle) (string, error) {
	if err := Validate(b); err != nil {
		return "", err
	}
	st, err := os.Lstat(parent)
	if err != nil || !filepath.IsAbs(parent) || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("receive parent must be an absolute private directory")
	}
	dir, err := os.MkdirTemp(parent, "platform-import-")
	if err != nil {
		return "", errors.New("cannot create receive directory")
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range Names {
		if err := config.AtomicPrivate(filepath.Join(dir, name), b[name]); err != nil {
			return "", err
		}
	}
	ok = true
	return dir, nil
}
