package sandbox

import (
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
		"http url":     func(i *Image) { i.URL = "http://x.example/u.img" },
		"todo hash":    func(i *Image) { i.SHA256 = "TODO" },
		"todo suffix":  func(i *Image) { i.SHA256 = "todo-fill-me-in" },
		"empty hash":   func(i *Image) { i.SHA256 = "" },
		"short hash":   func(i *Image) { i.SHA256 = "abc123" },
		"non-hex hash": func(i *Image) { i.SHA256 = strings.Repeat("z", 64) },
		"wrong arch":   func(i *Image) { i.Arch = "amd64" },
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
