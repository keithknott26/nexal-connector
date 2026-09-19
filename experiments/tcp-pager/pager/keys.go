package pager

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// KeySet is ephemeral in self-test. Init writes separate donor/client directories
// so only the client directory needs to be transferred to the receiving Mac.
type KeySet struct{ CA, ServerCert, ServerKey, ClientCert, ClientKey []byte }

func NewKeys() (*KeySet, error) {
	now := time.Now()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Nexal pager experiment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, priv)
	if err != nil {
		return nil, err
	}
	k := &KeySet{CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	issue := func(n int64, client bool) ([]byte, []byte, error) {
		p, s, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, nil, e
		}
		t := &x509.Certificate{SerialNumber: big.NewInt(n), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"nexal-donor"}}
		if client {
			t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			t.DNSNames = nil
		}
		d, e := x509.CreateCertificate(rand.Reader, t, ca, p, priv)
		if e != nil {
			return nil, nil, e
		}
		key, e := x509.MarshalPKCS8PrivateKey(s)
		if e != nil {
			return nil, nil, e
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), nil
	}
	k.ServerCert, k.ServerKey, err = issue(2, false)
	if err != nil {
		return nil, err
	}
	k.ClientCert, k.ClientKey, err = issue(3, true)
	return k, err
}
func TLSConfig(ca, cert, key []byte, server bool) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid CA")
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	// Strict hybrid key agreement, with ordinary Ed25519 certificate authentication.
	// This is not a claim of fully post-quantum authentication or RDMA encryption.
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{tls.X25519MLKEM768}, Certificates: []tls.Certificate{pair}}
	if server {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = pool
	} else {
		cfg.RootCAs = pool
		cfg.ServerName = "nexal-donor"
	}
	return cfg, nil
}
func (k *KeySet) ServerTLS() (*tls.Config, error) {
	return TLSConfig(k.CA, k.ServerCert, k.ServerKey, true)
}
func (k *KeySet) ClientTLS() (*tls.Config, error) {
	return TLSConfig(k.CA, k.ClientCert, k.ClientKey, false)
}

func InitKeys(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	} // Refuse existing destinations.
	k, err := NewKeys()
	if err != nil {
		return err
	}
	for _, role := range []string{"donor", "client"} {
		d := filepath.Join(dir, role)
		if err = os.Mkdir(d, 0700); err != nil {
			return err
		}
		cert, key := k.ServerCert, k.ServerKey
		if role == "client" {
			cert, key = k.ClientCert, k.ClientKey
		}
		for name, data := range map[string][]byte{"ca.pem": k.CA, "cert.pem": cert, "key.pem": key} {
			f, e := os.OpenFile(filepath.Join(d, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			e = writeFull(f, data)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}
func LoadTLS(dir string, server bool) (*tls.Config, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("key directory must be private (0700), not a symlink")
	}
	read := func(name string) ([]byte, error) {
		p := filepath.Join(dir, name)
		s, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 || s.Size() > 16384 {
			return nil, errors.New("key files must be regular, private and bounded")
		}
		return os.ReadFile(p)
	}
	ca, err := read("ca.pem")
	if err != nil {
		return nil, err
	}
	cert, err := read("cert.pem")
	if err != nil {
		return nil, err
	}
	key, err := read("key.pem")
	if err != nil {
		return nil, err
	}
	return TLSConfig(ca, cert, key, server)
}
