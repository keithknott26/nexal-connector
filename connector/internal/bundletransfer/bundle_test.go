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

func TestValidateRejectsMalformedConnectionJSON(t *testing.T) {
	good, _, _ := fixture(t)
	var base map[string]any
	if err := json.Unmarshal(good["connection.json"], &base); err != nil {
		t.Fatal(err)
	}
	with := func(edit func(m map[string]any)) []byte {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		edit(m)
		out, _ := json.Marshal(m)
		return out
	}
	cases := map[string][]byte{
		"wrong schema":     with(func(m map[string]any) { m["schemaVersion"] = 2 }),
		"wrong pages":      with(func(m map[string]any) { m["pages"] = 32 }),
		"expired":          with(func(m map[string]any) { m["expiresAt"] = time.Now().Add(-time.Minute).UTC() }),
		"fingerprint":      with(func(m map[string]any) { m["caSHA256"] = strings.Repeat("0", 64) }),
		"unknown field":    with(func(m map[string]any) { m["extra"] = true }),
		"loopback":         with(func(m map[string]any) { m["endpoint"] = "127.0.0.1:9443" }),
		"public":           with(func(m map[string]any) { m["endpoint"] = "8.8.8.8:9443" }),
		"hostname":         with(func(m map[string]any) { m["endpoint"] = "example.local:9443" }),
		"non-numeric port": with(func(m map[string]any) { m["endpoint"] = "192.168.1.20:https" }),
		"port too large":   with(func(m map[string]any) { m["endpoint"] = "192.168.1.20:70000" }),
		"trailing json":    append(append([]byte{}, good["connection.json"]...), []byte(` {}`)...),
	}
	for name, raw := range cases {
		b := Bundle{}
		for k, v := range good {
			b[k] = v
		}
		b["connection.json"] = raw
		if err := Validate(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Validate(good); err != nil {
		t.Fatalf("baseline rejected: %v", err)
	}
}

func TestValidateRejectsMissingOrOversizeFiles(t *testing.T) {
	good, _, _ := fixture(t)
	for _, name := range Names {
		b := Bundle{}
		for k, v := range good {
			b[k] = v
		}
		b[name] = nil
		if Validate(b) == nil {
			t.Errorf("empty %s accepted", name)
		}
		b[name] = make([]byte, MaxPlaintext+1)
		if Validate(b) == nil {
			t.Errorf("oversize %s accepted", name)
		}
	}
}

func TestInstallAndReadRejectUnsafeLocations(t *testing.T) {
	b, _, _ := fixture(t)
	open := t.TempDir()
	os.Chmod(open, 0755)
	if _, err := Install(open, b); err == nil {
		t.Fatal("group/world-readable parent accepted")
	}
	if entries, _ := os.ReadDir(open); len(entries) != 0 {
		t.Fatalf("rejected install left files behind: %v", entries)
	}
	if _, err := Install("relative/parent", b); err == nil {
		t.Fatal("relative parent accepted")
	}
	if _, err := Install(filepath.Join(open, "missing"), b); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err := Read("relative/dir"); err == nil {
		t.Fatal("relative bundle dir accepted")
	}
	bad := Bundle{"ca.pem": []byte("x")}
	private := t.TempDir()
	os.Chmod(private, 0700)
	if _, err := Install(private, bad); err == nil {
		t.Fatal("invalid bundle installed")
	}
	if entries, _ := os.ReadDir(private); len(entries) != 0 {
		t.Fatalf("invalid bundle left files behind: %v", entries)
	}
}
func TestSealRejectsBadBindingKeyAndOversizeBundle(t *testing.T) {
	b, tx, key := fixture(t)
	fp := Fingerprint(key.EncapsulationKey().Bytes())

	same := tx
	same.ReceiverHostID = same.DonorHostID
	if _, err := Seal(same, fp, b); err == nil {
		t.Fatal("donor and receiver on the same host accepted")
	}

	short := tx
	short.PublicKey = base64.StdEncoding.EncodeToString([]byte("not an ml-kem key"))
	if _, err := Seal(short, Fingerprint([]byte("not an ml-kem key")), b); err == nil ||
		!strings.Contains(err.Error(), "invalid recipient encryption key") {
		t.Fatalf("malformed recipient key = %v", err)
	}

	big := Bundle{}
	for name, data := range b {
		big[name] = data
	}
	big["extra.bin"] = make([]byte, MaxPlaintext)
	if _, err := Seal(tx, fp, big); err == nil {
		t.Fatal("oversize bundle accepted")
	}
}
