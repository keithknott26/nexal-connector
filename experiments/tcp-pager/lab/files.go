// Package lab automates disposable, owner-operated two-computer acceptance.
// It is isolated from the neXal production connector and all marketplace APIs.
package lab

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"nexal/experiments/tcp-pager/pager"
)

const (
	Pages   = 64
	MaxFile = 16384
)

type Manifest struct {
	Schema        int       `json:"schemaVersion"`
	Endpoint      string    `json:"endpoint"`
	Pages         int       `json:"pages"`
	CAFingerprint string    `json:"caSHA256"`
	Expires       time.Time `json:"expiresAt"`
}

// Addresses lists private interface IPs, not proof of a direct physical LAN.
func Addresses() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []string
	seen := map[string]bool{}
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.IsPrivate() && !seen[ip.String()] {
			seen[ip.String()] = true
			ips = append(ips, ip.String())
		}
	}
	return ips, nil
}

func ValidateEndpoint(addr string, loopback bool) error {
	if err := pager.PrivateAddress(addr); err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(addr)
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be 1..65535")
	}
	if net.ParseIP(host).IsLoopback() && !loopback {
		return errors.New("LAN acceptance refuses loopback; use explicit --loopback-test only for development")
	}
	return nil
}

func IsLocal(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, errors.New("numeric address required")
	}
	if ip.IsLoopback() {
		return true, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		other, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.Equal(other) {
			return true, nil
		}
	}
	return false, nil
}

// ReadFile rejects symlinks, oversized files and writable shared files. The
// transfer source may be readable after AirDrop; Import copies into 0700/0600
// storage before TLS use. The caller warns about deleting the transfer copy.
// Malicious same-user mutation during import is outside this lab's trust model.
func ReadFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0022 != 0 || fi.Size() > MaxFile {
		return nil, errors.New("bundle files must be bounded regular files, not symlinks or shared-writable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if len(b) > MaxFile {
		return nil, errors.New("file exceeds bound")
	}
	return b, err
}

func PrivateWrite(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return PrivateWrite(path, append(b, '\n'))
}

func LoadManifest(dir string, loopback bool) (Manifest, error) {
	var m Manifest
	fi, err := os.Lstat(dir)
	if err != nil {
		return m, err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0022 != 0 {
		return m, errors.New("bundle directory must not be a symlink or shared-writable")
	}
	b, err := ReadFile(filepath.Join(dir, "connection.json"))
	if err != nil {
		return m, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, errors.New("trailing manifest data")
	}
	if m.Schema != 1 || m.Pages != Pages || len(m.CAFingerprint) != 64 ||
		!time.Now().Before(m.Expires) {
		return m, errors.New("invalid or expired acceptance manifest")
	}
	if err = ValidateEndpoint(m.Endpoint, loopback); err != nil {
		return m, err
	}
	ca, err := ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return m, err
	}
	digest := sha256.Sum256(ca)
	if hex.EncodeToString(digest[:]) != m.CAFingerprint {
		return m, errors.New("bundle CA fingerprint mismatch")
	}
	block, rest := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return m, errors.New("invalid bundle CA")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return m, err
	}
	if !cert.IsCA || m.Expires.After(cert.NotAfter) || time.Now().Before(cert.NotBefore) {
		return m, errors.New("invalid CA lifetime")
	}
	return m, nil
}

// Prepare creates new credentials. It never overwrites an existing directory.
func Prepare(state, endpoint string, loopback bool) (Manifest, error) {
	var m Manifest
	if err := ValidateEndpoint(endpoint, loopback); err != nil {
		return m, err
	}
	local, err := IsLocal(endpoint)
	if err != nil {
		return m, err
	}
	if !local {
		return m, errors.New("donor address is not assigned to this computer")
	}
	if err = pager.InitKeys(state); err != nil {
		return m, err
	}
	ca, err := ReadFile(filepath.Join(state, "client", "ca.pem"))
	if err != nil {
		return m, err
	}
	block, _ := pem.Decode(ca)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return m, err
	}
	sum := sha256.Sum256(ca)
	m = Manifest{Schema: 1, Endpoint: endpoint, Pages: Pages,
		CAFingerprint: hex.EncodeToString(sum[:]), Expires: cert.NotAfter}
	for _, role := range []string{"client", "donor"} {
		if err = WriteJSON(filepath.Join(state, role, "connection.json"), m); err != nil {
			return m, err
		}
	}
	return m, nil
}

// Import accepts exactly the needed credential files, never scripts/binaries.
func Import(source, dest string, loopback bool) (Manifest, error) {
	m, err := LoadManifest(source, loopback)
	if err != nil {
		return m, err
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return m, err
	}
	for _, name := range []string{"ca.pem", "cert.pem", "key.pem", "connection.json"} {
		b, e := ReadFile(filepath.Join(source, name))
		if e != nil {
			return m, e
		}
		if e = PrivateWrite(filepath.Join(dest, name), b); e != nil {
			return m, e
		}
	}
	// Revalidate the copied data, not only the original manifest.
	copied, err := LoadManifest(dest, loopback)
	if err != nil {
		return m, err
	}
	if copied != m {
		return m, errors.New("manifest changed during import")
	}
	_, err = pager.LoadTLS(dest, false)
	return m, err
}
