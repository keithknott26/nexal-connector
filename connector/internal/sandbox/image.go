package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// MaxImageBytes bounds a single download.
const MaxImageBytes = 32 << 30

// Converter turns a non-raw disk image (qcow2) into a raw one, which is what the
// hypervisor needs. This is an interface only: a bundled qemu-img or a pure-Go
// converter plugs in here. With no Converter a qcow2 image is refused.
type Converter interface {
	ToRaw(ctx context.Context, src, dst string) error
}

// ImageStore caches verified base images.
//
// Cache layout, keyed by the image digest (see ImageDigest.CacheKey: the bare
// 64-hex SHA-256 for sha256, "sha512-<hex>" for sha512):
//
//	<key>.src  the downloaded file, byte-for-byte what the hash covers
//	<key>.raw  derived raw copy, present only when .src was qcow2
type ImageStore struct {
	Dir     string
	HTTP    *http.Client
	Convert Converter

	mu sync.Mutex
}

// DefaultImageDir is ~/Library/Application Support/Nexal/images.
func DefaultImageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Nexal", "images"), nil
}

// NewImageStore builds a store. The download client is deliberately NOT the
// coordinator client: image URLs are third-party mirrors and must never see the
// host's bearer token. Redirects are limited to https.
func NewImageStore(dir string, conv Converter) *ImageStore {
	return &ImageStore{Dir: dir, Convert: conv, HTTP: &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to non-https URL refused")
			}
			return nil
		},
	}}
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var sha512Pattern = regexp.MustCompile(`^[0-9a-f]{128}$`)

// ImageDigest is a parsed, validated image hash.
type ImageDigest struct {
	Algo string // "sha256" or "sha512"
	Hex  string // lowercase hex
}

// String is the "algo:hex" spelling.
func (d ImageDigest) String() string { return d.Algo + ":" + d.Hex }

// CacheKey is a filesystem-safe cache name. sha256 keeps the historic bare-hex
// name so existing caches stay valid.
func (d ImageDigest) CacheKey() string {
	if d.Algo == "sha256" {
		return d.Hex
	}
	return d.Algo + "-" + d.Hex
}

func (d ImageDigest) newHash() hash.Hash {
	if d.Algo == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

// ParseImageDigest validates "sha256:<64hex>" or "sha512:<128hex>".
func ParseImageDigest(v string) (ImageDigest, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	algo, hx, ok := strings.Cut(v, ":")
	if !ok {
		return ImageDigest{}, errors.New("image digest must look like sha256:<hex> or sha512:<hex>")
	}
	switch {
	case algo == "sha256" && sha256Pattern.MatchString(hx):
	case algo == "sha512" && sha512Pattern.MatchString(hx):
	case algo == "sha256" || algo == "sha512":
		return ImageDigest{}, errors.New("image digest is malformed; refusing")
	default:
		return ImageDigest{}, fmt.Errorf("unsupported image digest algorithm %q", algo)
	}
	return ImageDigest{Algo: algo, Hex: hx}, nil
}

// Digest resolves the image hash: the new "digest" field when present, else the
// legacy 64-hex "sha256" field. If both are present they must agree whenever the
// digest is sha256.
func (i Image) Digest() (ImageDigest, error) {
	if strings.TrimSpace(i.DigestStr) != "" {
		d, err := ParseImageDigest(i.DigestStr)
		if err != nil {
			return ImageDigest{}, err
		}
		if l := strings.ToLower(strings.TrimSpace(i.SHA256)); l != "" && d.Algo == "sha256" && l != d.Hex {
			return ImageDigest{}, errors.New("image digest and sha256 disagree; refusing")
		}
		return d, nil
	}
	h := strings.ToLower(strings.TrimSpace(i.SHA256))
	if h == "" || strings.Contains(h, "todo") {
		return ImageDigest{}, errors.New("image has no real SHA-256 (placeholder); refusing")
	}
	if !sha256Pattern.MatchString(h) {
		return ImageDigest{}, errors.New("image SHA-256 is malformed; refusing")
	}
	return ImageDigest{Algo: "sha256", Hex: h}, nil
}

// NormalizeArch maps vendor spellings to Go's GOARCH names.
func NormalizeArch(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "arm64", "aarch64":
		return "arm64"
	case "amd64", "x86_64", "x64":
		return "amd64"
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// HostArch is this process's architecture in catalog spelling.
func HostArch() string { return NormalizeArch(runtime.GOARCH) }

// ValidateImage refuses an image reference that must not be booted: non-https
// URL, missing/placeholder/malformed hash, wrong architecture for hostArch, or
// no cloud-init. It is pure.
func ValidateImage(img Image, hostArch string) error {
	u, err := url.Parse(img.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("image URL must be https")
	}
	if _, err := img.Digest(); err != nil {
		return err
	}
	if NormalizeArch(img.Arch) != NormalizeArch(hostArch) {
		return fmt.Errorf("image arch %q does not match this Mac (%s)", img.Arch, hostArch)
	}
	if !img.CloudInit {
		return errors.New("image does not support cloud-init; not supported yet")
	}
	return nil
}

// IsQCOW2 reports whether path starts with the qcow2 magic.
func IsQCOW2(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var m [4]byte
	if _, err := io.ReadFull(f, m[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(m[:], []byte{'Q', 'F', 'I', 0xfb}), nil
}

func hashFile(path string, d ImageDigest) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := d.newHash()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func digestEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RemoteSize asks the mirror for the image size (HEAD). It is best effort and
// returns 0 when unknown.
func (s *ImageStore) RemoteSize(ctx context.Context, rawURL string) int64 {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 {
		return 0
	}
	return resp.ContentLength
}

// Ensure returns the path of a verified, raw, ready-to-clone base image and its
// size, downloading and verifying it first when it is not cached. The cached
// download is re-hashed on every use, so a tampered cache file is never booted.
// Calls are serialized; two creates of the same image download it once.
func (s *ImageStore) Ensure(ctx context.Context, img Image) (string, int64, error) {
	if err := ValidateImage(img, HostArch()); err != nil {
		return "", 0, err
	}
	dg, err := img.Digest()
	if err != nil {
		return "", 0, err
	}
	want := dg.CacheKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", 0, err
	}
	src := filepath.Join(s.Dir, want+".src")
	if got, _, err := hashFile(src, dg); err == nil && digestEqual(got, dg.Hex) {
		// verified cache hit
	} else {
		if err == nil {
			_ = os.Remove(src) // present but wrong: discard
		}
		if err := s.download(ctx, img.URL, src, dg); err != nil {
			return "", 0, err
		}
	}
	isQ, err := IsQCOW2(src)
	if err != nil {
		return "", 0, err
	}
	usable := src
	if isQ {
		raw := filepath.Join(s.Dir, want+".raw")
		if fi, err := os.Stat(raw); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			if s.Convert == nil {
				return "", 0, errors.New("image is qcow2 and no converter is available")
			}
			progressOf(ctx)(StepConvert, 60)
			part := raw + ".part"
			_ = os.Remove(part)
			if err := s.Convert.ToRaw(ctx, src, part); err != nil {
				_ = os.Remove(part)
				return "", 0, fmt.Errorf("qcow2 to raw conversion failed: %w", err)
			}
			if err := os.Rename(part, raw); err != nil {
				return "", 0, err
			}
		}
		usable = raw
	}
	fi, err := os.Stat(usable)
	if err != nil {
		return "", 0, err
	}
	return usable, fi.Size(), nil
}

// countingWriter maps bytes received onto the 5..58 % band of overall progress.
type countingWriter struct {
	n, total int64
	report   ProgressFunc
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.total > 0 {
		c.report(StepDownload, 5+int(53*c.n/c.total))
	} else {
		c.report(StepDownload, 5)
	}
	return len(p), nil
}

func (s *ImageStore) download(ctx context.Context, rawURL, dest string, want ImageDigest) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("cannot construct image request")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return errors.New("image download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image download failed: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxImageBytes {
		return errors.New("image is larger than the allowed maximum")
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := want.newHash()
	progress := progressOf(ctx)
	total := resp.ContentLength
	n, err := io.Copy(io.MultiWriter(f, h, &countingWriter{total: total, report: progress}), io.LimitReader(resp.Body, MaxImageBytes+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return errors.New("image download interrupted")
	}
	if n > MaxImageBytes {
		_ = os.Remove(part)
		return errors.New("image is larger than the allowed maximum")
	}
	if !digestEqual(hex.EncodeToString(h.Sum(nil)), want.Hex) {
		_ = os.Remove(part)
		return fmt.Errorf("image %s mismatch; refusing to use it", strings.ToUpper(want.Algo))
	}
	return os.Rename(part, dest)
}

// CloneDisk makes the per-sandbox copy-on-write disk with an APFS clone
// (`cp -c`, clonefile(2)), then grows the sparse file to diskGB when larger than
// the base. It fails rather than falling back to a full copy: a silent
// multi-gigabyte copy is exactly what the disk cap exists to prevent. The image
// cache and the sandbox directory must therefore be on the same APFS volume.
func CloneDisk(ctx context.Context, base, dst string, diskGB int) error {
	out, err := exec.CommandContext(ctx, "/bin/cp", "-c", base, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("apfs clone failed (same APFS volume required): %v: %s", err, trimOutput(out))
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if want := int64(diskGB) << 30; want > fi.Size() {
		if err := os.Truncate(dst, want); err != nil {
			return err
		}
	}
	return nil
}

func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "")
	}
	return s
}
