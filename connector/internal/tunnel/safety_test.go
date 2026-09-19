package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAmbiguousOrUnsupportedDiagnosticsFailClosed(t *testing.T) {
	for name, line := range map[string]string{
		"duplicate_protocol": `{"message":"Registered tunnel connection","protocol":"http2","protocol":"quic"}`,
		"case_alias":         `{"protocol":"http2","PROTOCOL":"quic"}`,
		"unknown_protocol":   `{"protocol":"h2"}`,
		"null_protocol":      `{"protocol":null}`,
		"nonstring_protocol": `{"protocol":123}`,
		"unknown_group":      `{"keyAgreement":"P384"}`,
		"nonstring_group":    `{"curve":123}`,
		"null_group":         `{"curve":null}`,
		"malformed":          `not structured diagnostics`,
		"oversize":           strings.Repeat("x", 65537),
		"protocol_secret":    `{"message":"post-quantum disabled","protocol":"credential-do-not-echo"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := Observe(Evidence{Configured: true, Connected: true, Verified: true, Attestation: true}, []byte(line), time.Now())
			if !e.Quarantined || e.Connected || e.Verified || e.Attestation {
				t.Fatal("unsafe diagnostics failed open")
			}
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "credential-do-not-echo") {
				t.Fatal("raw diagnostic leaked into evidence")
			}
			e = Observe(e, []byte(`{"message":"Registered tunnel connection","protocol":"quic"}`), time.Now())
			if e.Connected || !e.Quarantined || e.Verified || e.Attestation {
				t.Fatal("reconnect bypassed quarantine")
			}
		})
	}
}

func TestStrictSupervisorQuarantinesLocalFixtures(t *testing.T) {
	// Only an inert shell fixture is executed: no cloudflared, credentials,
	// network connection, download, tunnel, or external spending.
	for name, line := range map[string]string{
		"downgrade": `{"message":"Registered tunnel connection","protocol":"http2"}`,
		"ambiguous": `{"protocol":"http2","protocol":"quic"}`,
		"malformed": `unstructured output`,
	} {
		t.Run(name, func(t *testing.T) {
			pin := fixture(t)
			script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'cloudflared version 2026.1.0 (test)'; exit 0; fi\nprintf '%s\\n' '" + line + "'\nexec /bin/sleep 30\n"
			if err := os.WriteFile(pin.Binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(script))
			pin.SHA256 = hex.EncodeToString(sum[:])
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var last Evidence
			err := Run(ctx, pin, "127.0.0.1:8788", filepath.Join(t.TempDir(), "private"), func(e Evidence) { last = e })
			if err == nil || ctx.Err() != nil || !last.Quarantined || last.Connected || last.Verified || last.Attestation {
				t.Fatalf("supervisor did not stop on unsafe diagnostics: %v", err)
			}
		})
	}
}
