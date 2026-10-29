package tunnel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

// TestValidateRejectsEachPinDefect proves every individual defect in the pin
// is refused with its own reason, so one check cannot silently shadow another.
func TestValidateRejectsEachPinDefect(t *testing.T) {
	const listen = "127.0.0.1:8788"
	arch := "amd64"
	if runtime.GOARCH == "amd64" {
		arch = "arm64"
	}
	cases := []struct {
		name   string
		mutate func(t *testing.T, p *config.Tunnel)
		want   string
	}{
		{"relative token path", func(_ *testing.T, p *config.Tunnel) { p.TokenFile = "tunnel.token" }, "must be absolute"},
		{"short digest", func(_ *testing.T, p *config.Tunnel) { p.SHA256 = "abcd" }, "SHA256 required"},
		{"non-hex digest", func(_ *testing.T, p *config.Tunnel) { p.SHA256 = strings.Repeat("z", 64) }, "SHA256 required"},
		{"non-calendar version", func(_ *testing.T, p *config.Tunnel) { p.Version = "latest" }, "release version"},
		{"architecture mismatch", func(_ *testing.T, p *config.Tunnel) { p.Architecture = arch }, "architecture"},
		{"http source", func(_ *testing.T, p *config.Tunnel) { p.SourceURL = strings.Replace(p.SourceURL, "https", "http", 1) }, "source URL"},
		{"source for other version", func(_ *testing.T, p *config.Tunnel) {
			p.SourceURL = strings.Replace(p.SourceURL, "/2026.1.0/", "/2025.1.0/", 1)
		}, "source URL"},
		{"source with query", func(_ *testing.T, p *config.Tunnel) { p.SourceURL += "?x=1" }, "source URL"},
		{"blank verification method", func(_ *testing.T, p *config.Tunnel) { p.VerificationMethod = "  " }, "verification method"},
		{"oversized verification method", func(_ *testing.T, p *config.Tunnel) { p.VerificationMethod = strings.Repeat("v", 513) }, "verification method"},
		{"dotless hostname", func(_ *testing.T, p *config.Tunnel) { p.Hostname = "localhost" }, "hostname"},
		{"uppercase hostname", func(_ *testing.T, p *config.Tunnel) { p.Hostname = "Host.Example.test" }, "hostname"},
		{"double-dot hostname", func(_ *testing.T, p *config.Tunnel) { p.Hostname = "host..example.test" }, "hostname"},
		{"missing binary", func(_ *testing.T, p *config.Tunnel) { p.Binary = filepath.Join(filepath.Dir(p.Binary), "gone") }, "pinned binary must be regular"},
		{"world-writable binary", func(t *testing.T, p *config.Tunnel) {
			if err := os.Chmod(p.Binary, 0702); err != nil {
				t.Fatal(err)
			}
		}, "pinned binary must be regular"},
		{"non-executable binary", func(t *testing.T, p *config.Tunnel) {
			if err := os.Chmod(p.Binary, 0600); err != nil {
				t.Fatal(err)
			}
		}, "pinned binary must be regular"},
		{"short token", func(t *testing.T, p *config.Tunnel) {
			if err := os.WriteFile(p.TokenFile, []byte("short"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "invalid cloudflared token"},
		{"token with inner whitespace", func(t *testing.T, p *config.Tunnel) {
			if err := os.WriteFile(p.TokenFile, []byte(strings.Repeat("t", 20)+" "+strings.Repeat("t", 20)), 0600); err != nil {
				t.Fatal(err)
			}
		}, "invalid cloudflared token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fixture(t)
			c.mutate(t, &p)
			err := Validate(p, listen)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error containing %q", err, c.want)
			}
		})
	}
	if err := Validate(fixture(t), listen); err != nil {
		t.Fatalf("unmutated fixture rejected: %v", err)
	}
}
