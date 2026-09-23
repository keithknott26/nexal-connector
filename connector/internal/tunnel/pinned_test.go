package tunnel

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every rule Validate() applies to the artifact half of the config, applied to the
// pin file itself. Because pinned.json is embedded, editing it invalidates the test
// cache and these run again -- which was not true of the shell script this replaced.
func TestPinnedSatisfiesValidate(t *testing.T) {
	p, err := Pinned()
	if err != nil {
		t.Fatalf("Pinned(): %v", err)
	}
	if !versionRE.MatchString(p.Version) {
		t.Errorf("version %q would be rejected by Validate (a leading \"v\" is the usual cause: the git tag has one, the release version does not)", p.Version)
	}
	d, err := hex.DecodeString(p.SHA256)
	if err != nil || len(d) != 32 {
		t.Errorf("sha256 %q is not 32 hex-encoded bytes", p.SHA256)
	}
	if p.SHA256 != strings.ToLower(p.SHA256) {
		t.Error("sha256 must be lowercase; Validate compares case-insensitively but the script uses shasum output directly")
	}
	if m := strings.TrimSpace(p.VerificationMethod); m == "" || len(m) > 512 {
		t.Errorf("verificationMethod is empty or over Validate's 512-byte bound (%d)", len(p.VerificationMethod))
	}
	// This field is the provenance record. Cloudflare ships no checksum, signature,
	// SBOM or attestation for this asset -- both the release assets and GitHub's
	// attestations API were checked. An entry that omits that is claiming assurance
	// nobody actually provided.
	low := strings.ToLower(p.VerificationMethod)
	if !strings.Contains(low, "no checksum") && !strings.Contains(low, "no independent") {
		t.Error("verificationMethod must state that no publisher checksum or signature backs this pin")
	}
}

// Pinned() is the only place version, asset and URL are reconciled. These are the
// two mistakes it exists to catch.
func TestPinnedRejectsInconsistentArtifact(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"version bumped, url left behind": func(s string) string {
			return strings.Replace(s, `"version": "2026.9.1"`, `"version": "2026.10.2"`, 1)
		},
		"url points at a different asset": func(s string) string {
			return strings.Replace(s, "download/2026.9.1/cloudflared-darwin-arm64.tgz", "download/2026.9.1/cloudflared-darwin-amd64.tgz", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := pinnedFS.ReadFile("pinned.json")
			if err != nil {
				t.Fatal(err)
			}
			mutated := mutate(string(raw))
			if mutated == string(raw) {
				t.Fatal("mutation did not apply; pinned.json shape changed and this test is no longer checking anything")
			}
			// Parsed through the same reconciliation Pinned() performs.
			if err := checkArtifact(mutated); err == nil {
				t.Error("inconsistent artifact accepted")
			}
		})
	}
}

// The provisioning script must consume pinned.json rather than restating it, or the
// duplication -- and the stale-cache problem -- comes straight back.
func TestProvisioningScriptConsumesThePinFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "setup-cloudflared-origin.sh"))
	if err != nil {
		t.Fatalf("cannot read provisioning script: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "pinned.json") {
		t.Error("script must read pinned.json instead of hardcoding the version, digest and URL")
	}
	// Launching is BuildArgs' job: QUIC, --post-quantum, the generated config and the
	// token FILE are the policy. A second launch path is a second policy, and the
	// weaker one would be the one actually running.
	for _, forbidden := range []string{"tunnel run", "--token ", "service install", "launchctl"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("script contains %q: it must not launch cloudflared, and a token must never be a command argument", forbidden)
		}
	}
}
