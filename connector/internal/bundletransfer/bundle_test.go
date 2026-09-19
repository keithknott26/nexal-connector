package bundletransfer

import (
	"crypto/ed25519"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/client"
)

func fixture(t *testing.T) (Bundle, client.Transfer, *mlkem.DecapsulationKey768) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute),
		NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := x509.MarshalPKCS8PrivateKey(priv)
	b := Bundle{
		"ca.pem":   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}),
	}
	b["connection.json"], _ = json.Marshal(map[string]any{"schemaVersion": 1, "pages": 64,
		"endpoint": "192.168.1.20:9443", "caSHA256": Fingerprint(b["ca.pem"]),
		"expiresAt": now.Add(30 * time.Minute).UTC()})
	key, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	return b, client.Transfer{ID: "transfer_test", DonorHostID: "host_donor", ReceiverHostID: "host_receiver",
		PublicKey: base64.StdEncoding.EncodeToString(key.EncapsulationKey().Bytes()),
		ExpiresAt: now.Add(10 * time.Minute).UTC().Format(time.RFC3339Nano), State: "ready"}, key
}
func TestRoundTripAndPrivateInstall(t *testing.T) {
	b, tx, key := fixture(t)
	sealed, err := Seal(tx, Fingerprint(key.EncapsulationKey().Bytes()), b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "PRIVATE KEY") {
		t.Fatal("plaintext leaked")
	}
	tx.Ciphertext = &sealed
	got, err := Open(tx, key)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := Install(parent, got)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		if string(data) != string(b[name]) {
			t.Fatal(name)
		}
		st, _ := os.Stat(filepath.Join(dir, name))
		if st.Mode().Perm() != 0600 {
			t.Fatal("permissions")
		}
	}
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
	second, err := Install(parent, got)
	if err != nil || second == dir {
		t.Fatal("must not overwrite")
	}
}
func TestFingerprintRequired(t *testing.T) {
	b, tx, _ := fixture(t)
	if _, err := Seal(tx, strings.Repeat("0", 64), b); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
}
func TestBindingAndTamper(t *testing.T) {
	b, tx, key := fixture(t)
	sealed, err := Seal(tx, Fingerprint(key.EncapsulationKey().Bytes()), b)
	if err != nil {
		t.Fatal(err)
	}
	tx.Ciphertext = &sealed
	for _, modify := range []func(*client.Transfer){
		func(v *client.Transfer) { v.ID = "transfer_other" },
		func(v *client.Transfer) { v.ReceiverHostID = "host_other" },
		func(v *client.Transfer) { v.DonorHostID = "host_other" },
		func(v *client.Transfer) { v.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339Nano) },
	} {
		copy := tx
		modify(&copy)
		if _, err := Open(copy, key); err == nil {
			t.Fatal("changed identity accepted")
		}
	}
	wire, _ := base64.StdEncoding.DecodeString(sealed)
	wire[len(wire)-1] ^= 1
	bad := base64.StdEncoding.EncodeToString(wire)
	tx.Ciphertext = &bad
	if _, err := Open(tx, key); err == nil {
		t.Fatal("tamper accepted")
	}
}
func TestWrongReceiverAndOversize(t *testing.T) {
	b, tx, key := fixture(t)
	sealed, _ := Seal(tx, Fingerprint(key.EncapsulationKey().Bytes()), b)
	tx.Ciphertext = &sealed
	other, _ := mlkem.GenerateKey768()
	if _, err := Open(tx, other); err == nil {
		t.Fatal("wrong receiver")
	}
	large := strings.Repeat("A", 12004)
	tx.Ciphertext = &large
	if _, err := Open(tx, key); err == nil {
		t.Fatal("oversize accepted")
	}
}
func TestRejectExtraFilesAndUnsafeSource(t *testing.T) {
	b, _, _ := fixture(t)
	parent := t.TempDir()
	os.Chmod(parent, 0700)
	dir, err := Install(parent, b)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "extra"), []byte("no"), 0600)
	if _, err := Read(dir); err == nil {
		t.Fatal("extra accepted")
	}
	os.Remove(filepath.Join(dir, "extra"))
	os.Chmod(filepath.Join(dir, "key.pem"), 0644)
	if _, err := Read(dir); err == nil {
		t.Fatal("readable key accepted")
	}
	os.Chmod(filepath.Join(dir, "key.pem"), 0600)
	link := filepath.Join(parent, "link")
	os.Symlink(dir, link)
	if _, err := Read(link); err == nil {
		t.Fatal("symlink directory")
	}
	b["../escape"] = []byte("no")
	if err := Validate(b); err == nil {
		t.Fatal("extra bundle key")
	}
}
func TestRejectPublicEndpointAndBadCertificate(t *testing.T) {
	for _, mode := range []string{"public", "loopback", "expiry", "certificate", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			b, _, _ := fixture(t)
			var m map[string]any
			json.Unmarshal(b["connection.json"], &m)
			switch mode {
			case "public":
				m["endpoint"] = "8.8.8.8:9443"
			case "loopback":
				m["endpoint"] = "127.0.0.1:9443"
			case "expiry":
				m["expiresAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
			case "certificate":
				b["cert.pem"] = []byte("invalid")
			}
			b["connection.json"], _ = json.Marshal(m)
			if mode == "duplicate" {
				b["connection.json"] = []byte(`{"pages":64,"pages":64}`)
			}
			if err := Validate(b); err == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
