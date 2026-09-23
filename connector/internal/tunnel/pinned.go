package tunnel

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"nexal/connector/internal/config"
)

// The pinned cloudflared build lives in pinned.json, embedded here, and is read by
// BOTH this package and scripts/setup-cloudflared-origin.sh.
//
// It started as constants duplicated between the two. That is a bug rather than a
// style problem: a test that reads the shell script cannot be cached correctly by the
// go tool, because the script sits outside the module root and is therefore invisible
// to cache invalidation. Such a test passes forever on a stale result -- including in
// CI, which runs plain `go test -race`. Embedding the pins makes them a tracked input,
// so changing them rebuilds and re-runs everything that depends on them.
//
//go:embed pinned.json
var pinnedFS embed.FS

type pinned struct {
	Version            string `json:"version"`
	Architecture       string `json:"architecture"`
	Asset              string `json:"asset"`
	SourceURL          string `json:"sourceUrl"`
	SHA256             string `json:"sha256"`
	VerificationMethod string `json:"verificationMethod"`
}

func loadPinned() (pinned, error) {
	b, err := pinnedFS.ReadFile("pinned.json")
	if err != nil {
		return pinned{}, err
	}
	return parsePinned(string(b))
}

func parsePinned(raw string) (pinned, error) {
	var p pinned
	dec := json.NewDecoder(strings.NewReader(raw))
	// An unknown field means the pin file and this struct have diverged, which is
	// exactly the drift this file exists to prevent.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("pinned.json: %w", err)
	}
	return p, nil
}

// checkArtifact reconciles version, asset and sourceUrl for arbitrary pin bytes, so
// the consistency rules can be exercised directly.
func checkArtifact(raw string) error {
	p, err := parsePinned(raw)
	if err != nil {
		return err
	}
	return p.consistent()
}

func (p pinned) consistent() error {
	if p.Architecture != "arm64" {
		return errors.New("pinned.json: this origin is provisioned for Apple Silicon")
	}
	want := "https://github.com/cloudflare/cloudflared/releases/download/" + p.Version + "/" + p.Asset
	if p.SourceURL != want {
		return fmt.Errorf("pinned.json: sourceUrl does not match version %s and asset %s", p.Version, p.Asset)
	}
	if p.Asset != "cloudflared-darwin-"+p.Architecture+".tgz" {
		return errors.New("pinned.json: asset does not match architecture")
	}
	return nil
}

// Pinned returns the operator-independent half of the tunnel config: the artifact
// identity. Binary, TokenFile and Hostname are deliberately left empty because they
// are properties of the host, not of the release, and Validate() will reject the
// result until the operator supplies them.
func Pinned() (config.Tunnel, error) {
	p, err := loadPinned()
	if err != nil {
		return config.Tunnel{}, err
	}
	// Checked at load rather than only in Validate() so a bad pin fails on every host,
	// not just the architecture that happens to mismatch. The motivating failure is
	// bumping the version without re-deriving the URL, or vice versa.
	if err := p.consistent(); err != nil {
		return config.Tunnel{}, err
	}
	return config.Tunnel{
		SHA256:             p.SHA256,
		Version:            p.Version,
		Architecture:       p.Architecture,
		SourceURL:          p.SourceURL,
		VerificationMethod: p.VerificationMethod,
	}, nil
}

// PinnedMatchesHost reports whether the pinned architecture is the one Validate()
// will compare against on this machine, so a caller can say so plainly instead of
// surfacing "architecture does not match this host" from deep inside validation.
func PinnedMatchesHost() bool {
	p, err := loadPinned()
	return err == nil && p.Architecture == runtime.GOARCH
}
