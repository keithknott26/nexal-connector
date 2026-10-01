package sandbox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// an ed25519 public key blob: len("ssh-ed25519") "ssh-ed25519" len(32) 32 bytes.
func testHostKey() (key, fp string) {
	blob := []byte{0, 0, 0, 11}
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	for i := 0; i < 32; i++ {
		blob = append(blob, byte(i+1))
	}
	sum := sha256.Sum256(blob)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob), "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func TestParseFirstBootHostKey(t *testing.T) {
	key, fp := testHostKey()
	fb, ok, _, _ := ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7","hostKeyFingerprint":"` + fp + `","hostKey":"` + key + `"}`)
	if !ok || fb.HostKey != key || fb.HostKeyFingerprint != fp {
		t.Fatalf("got %+v ok=%v", fb, ok)
	}
	// key alone: the fingerprint is derived; a comment is dropped
	fb, ok, _, _ = ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7","hostKey":"` + key + ` root@box"}`)
	if !ok || fb.HostKey != key || fb.HostKeyFingerprint != fp {
		t.Fatalf("derived: got %+v ok=%v", fb, ok)
	}
	// no hostKey is still a valid (older guest) report
	if _, ok, _, _ = ParseFirstBootLine(`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7"}`); !ok {
		t.Fatal("report without hostKey rejected")
	}
	for name, line := range map[string]string{
		"mismatch":   `NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7","hostKeyFingerprint":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","hostKey":"` + key + `"}`,
		"wrong type": `NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7","hostKey":"ssh-rsa AAAAB3NzaC1yc2E"}`,
		"bad base64": `NEXAL-FIRSTBOOT {"meshIp":"100.64.1.7","hostKey":"ssh-ed25519 !!!"}`,
	} {
		if _, ok, _, _ := ParseFirstBootLine(line); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStateReportWireNames(t *testing.T) {
	b, err := json.Marshal(StateReport{SandboxID: "s", State: StateRunning, NeedsKey: true, AckTaskID: "t1", HostKey: "ssh-ed25519 AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ackTaskId":"t1"`, `"needsKey":true`, `"hostKey":"ssh-ed25519 AAAA"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing from %s", want, b)
		}
	}
	if strings.Contains(string(b), "ackedTask") {
		t.Errorf("legacy name still sent: %s", b)
	}
}
