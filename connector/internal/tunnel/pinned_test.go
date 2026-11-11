package tunnel

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
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

// PinnedMatchesHost must agree with the field it summarises, so a caller that
// trusts it never disagrees with Validate()'s architecture check.
func TestPinnedMatchesHostAgreesWithPinnedArchitecture(t *testing.T) {
	p, err := Pinned()
	if err != nil {
		t.Fatalf("Pinned(): %v", err)
	}
	if got, want := PinnedMatchesHost(), p.Architecture == runtime.GOARCH; got != want {
		t.Errorf("PinnedMatchesHost() = %v, want %v (pinned %q, host %q)", got, want, p.Architecture, runtime.GOARCH)
	}
}

// A pin file that has drifted from the struct (extra field) or is not JSON at all
// must fail loudly rather than be silently half-read.
func TestParsePinnedRejectsUnknownFieldsAndMalformedJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":  `{"version":"1","architecture":"arm64","bogus":true}`,
		"malformed json": `{"version":`,
		"empty":          ``,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePinned(raw); err == nil {
				t.Error("parsePinned accepted a bad pin file")
			} else if !strings.Contains(err.Error(), "pinned.json") {
				t.Errorf("error %q should name pinned.json", err)
			}
		})
	}
}

// The accepting path of the reconciliation: a self-consistent pin passes, so the
// rejection tests above cannot be passing merely because everything is rejected.
func TestCheckArtifactAcceptsConsistentPin(t *testing.T) {
	raw := `{"version":"1.2.3","architecture":"arm64","asset":"cloudflared-darwin-arm64.tgz",` +
		`"sourceUrl":"https://github.com/cloudflare/cloudflared/releases/download/1.2.3/cloudflared-darwin-arm64.tgz"}`
	if err := checkArtifact(raw); err != nil {
		t.Errorf("consistent pin rejected: %v", err)
	}
	if err := checkArtifact(strings.Replace(raw, `"arm64","asset"`, `"amd64","asset"`, 1)); err == nil {
		t.Error("non-arm64 pin accepted")
	}
}

// Drift between the pin file and the struct that reads it, a malformed file and an
// unsupported architecture must all be rejected rather than silently accepted.
func TestCheckArtifactRejectsDriftMalformedAndWrongArchitecture(t *testing.T) {
	raw, err := pinnedFS.ReadFile("pinned.json")
	if err != nil {
		t.Fatal(err)
	}
	good := string(raw)
	if err := checkArtifact(good); err != nil {
		t.Fatalf("embedded pin rejected: %v", err)
	}
	for name, in := range map[string]string{
		"unknown field":      strings.Replace(good, `"version"`, `"surprise": "x", "version"`, 1),
		"malformed json":     "{not json",
		"empty input":        "",
		"non-arm64 pin file": strings.Replace(good, `"architecture": "arm64"`, `"architecture": "amd64"`, 1),
	} {
		if in == good {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if err := checkArtifact(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
