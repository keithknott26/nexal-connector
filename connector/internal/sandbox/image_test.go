package sandbox

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goodImage() Image {
	return Image{URL: "https://cloud-images.example.org/u.img", SHA256: strings.Repeat("a", 64), Arch: "arm64", CloudInit: true}
}

func TestValidateImage(t *testing.T) {
	if err := ValidateImage(goodImage(), "arm64"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Image){
		"http url":      func(i *Image) { i.URL = "http://x.example/u.img" },
		"todo hash":     func(i *Image) { i.SHA256 = "TODO" },
		"todo suffix":   func(i *Image) { i.SHA256 = "todo-fill-me-in" },
		"empty hash":    func(i *Image) { i.SHA256 = "" },
		"short hash":    func(i *Image) { i.SHA256 = "abc123" },
		"non-hex hash":  func(i *Image) { i.SHA256 = strings.Repeat("z", 64) },
		"wrong arch":    func(i *Image) { i.Arch = "amd64" },
		"no cloud-init": func(i *Image) { i.CloudInit = false },
	}
	for name, mut := range cases {
		img := goodImage()
		mut(&img)
		if err := ValidateImage(img, "arm64"); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
	upper := goodImage()
	upper.SHA256 = strings.ToUpper(upper.SHA256)
	upper.Arch = "aarch64"
	if err := ValidateImage(upper, "arm64"); err != nil {
		t.Errorf("uppercase hash and aarch64 spelling should be accepted: %v", err)
	}
}

func TestNormalizeArch(t *testing.T) {
	for in, want := range map[string]string{"aarch64": "arm64", "ARM64": "arm64", "x86_64": "amd64", "amd64": "amd64"} {
		if got := NormalizeArch(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestIsQCOW2(t *testing.T) {
	dir := t.TempDir()
	q := filepath.Join(dir, "q")
	r := filepath.Join(dir, "r")
	e := filepath.Join(dir, "e")
	if err := os.WriteFile(q, []byte{'Q', 'F', 'I', 0xfb, 0, 0, 0, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r, []byte("rawrawraw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{q: true, r: false, e: false} {
		got, err := IsQCOW2(path)
		if err != nil || got != want {
			t.Errorf("%s: got %v err %v want %v", filepath.Base(path), got, err, want)
		}
	}
}

func TestParseImageDigest(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 128)
	d, err := ParseImageDigest(" SHA256:" + strings.ToUpper(a))
	if err != nil || d.Algo != "sha256" || d.Hex != a || d.CacheKey() != a {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = ParseImageDigest("sha512:" + b)
	if err != nil || d.CacheKey() != "sha512-"+b || d.String() != "sha512:"+b {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"", a, "sha256:" + b, "sha512:" + a, "md5:" + a, "sha256:", "sha256:" + strings.Repeat("g", 64), "sha512:../" + b[3:]} {
		if _, err := ParseImageDigest(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestImageDigestResolution(t *testing.T) {
	a := strings.Repeat("a", 64)
	img := Image{URL: "https://x.example/u.raw", Arch: "arm64", CloudInit: true}
	// digest only
	img.DigestStr = "sha512:" + strings.Repeat("c", 128)
	if err := ValidateImage(img, "arm64"); err != nil {
		t.Fatal(err)
	}
	// digest wins over a legacy sha256 that is a different algorithm's companion
	img.SHA256 = a
	if err := ValidateImage(img, "arm64"); err != nil {
		t.Fatal(err)
	}
	// sha256 digest must agree with the legacy field
	img.DigestStr = "sha256:" + strings.Repeat("d", 64)
	if err := ValidateImage(img, "arm64"); err == nil {
		t.Fatal("disagreeing sha256/digest accepted")
	}
	img.DigestStr = "sha256:" + a
	if err := ValidateImage(img, "arm64"); err != nil {
		t.Fatal(err)
	}
	img.DigestStr = "sha1:abc"
	if err := ValidateImage(img, "arm64"); err == nil {
		t.Fatal("bad digest accepted")
	}
	// legacy only is unchanged
	img.DigestStr = ""
	if d, err := img.Digest(); err != nil || d.CacheKey() != a {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestEnsureDigests(t *testing.T) {
	body := []byte("raw disk image bytes")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	s256 := sha256.Sum256(body)
	s512 := sha512.Sum512(body)
	hex256, hex512 := hex.EncodeToString(s256[:]), hex.EncodeToString(s512[:])
	wrong := strings.Repeat("0", 128)
	mk := func(sha, digest string) Image {
		return Image{URL: srv.URL + "/u.raw", SHA256: sha, DigestStr: digest, Arch: HostArch(), CloudInit: true}
	}
	for name, tc := range map[string]struct {
		img  Image
		ok   bool
		file string
	}{
		"legacy sha256":   {mk(hex256, ""), true, hex256 + ".src"},
		"digest sha256":   {mk("", "sha256:"+hex256), true, hex256 + ".src"},
		"digest sha512":   {mk("", "sha512:"+hex512), true, "sha512-" + hex512 + ".src"},
		"sha512 mismatch": {mk("", "sha512:"+wrong), false, ""},
		"sha256 mismatch": {mk(wrong[:64], ""), false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewImageStore(dir, nil)
			st.HTTP = srv.Client()
			path, size, err := st.Ensure(context.Background(), tc.img)
			if !tc.ok {
				if err == nil {
					t.Fatal("expected mismatch refusal")
				}
				if ents, _ := os.ReadDir(dir); len(ents) != 0 {
					t.Fatalf("mismatched download left in cache: %v", ents)
				}
				return
			}
			if err != nil || size != int64(len(body)) || filepath.Base(path) != tc.file {
				t.Fatalf("path=%s size=%d err=%v", path, size, err)
			}
			// tampered cache is discarded and re-downloaded
			if err := os.WriteFile(path, []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, size, err := st.Ensure(context.Background(), tc.img); err != nil || size != int64(len(body)) {
				t.Fatalf("size=%d err=%v", size, err)
			}
		})
	}
}
