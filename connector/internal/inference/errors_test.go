package inference

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAsError(t *testing.T) {
	orig := errf("some_code", "do the fix", "bad %s", "thing")
	if got := AsError(orig); got != orig {
		t.Fatalf("AsError should return the same *Error, got %+v", got)
	}
	if got := AsError(fmt.Errorf("wrapped: %w", orig)); got != orig {
		t.Fatalf("AsError should unwrap, got %+v", got)
	}
	got := AsError(errors.New("disk full"))
	if got.Code != "io_error" || got.Message != "disk full" || got.Fix == "" {
		t.Fatalf("unknown errors should become io_error with a fix, got %+v", got)
	}
	if orig.Error() != "bad thing" || orig.Fix != "do the fix" {
		t.Fatalf("errf fields wrong: %+v", orig)
	}
}

func TestCheckNoDuplicateKeys(t *testing.T) {
	ok := []string{`{"a":1,"b":{"c":2}}`, `[{"a":1},{"a":2}]`, `"x"`, `{"a":[1,2,{"b":1}]}`}
	for _, s := range ok {
		if err := CheckNoDuplicateKeys([]byte(s)); err != nil {
			t.Errorf("%s: unexpected error %v", s, err)
		}
	}
	bad := []string{`{"a":1,"a":2}`, `{"x":{"a":1,"a":2}}`, `[{"a":1,"a":1}]`, `{"a":1} {"b":2}`, `{"a":1`, ``}
	for _, s := range bad {
		if err := CheckNoDuplicateKeys([]byte(s)); err == nil {
			t.Errorf("%s: expected an error", s)
		}
	}
}

func TestDecodeStrict(t *testing.T) {
	type v struct {
		A int `json:"a"`
	}
	var out v
	if err := DecodeStrict([]byte(`{"a":3}`), &out); err != nil || out.A != 3 {
		t.Fatalf("valid decode failed: %v %+v", err, out)
	}
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":1,"zzz":2}`, `{"a":1}x`} {
		if err := DecodeStrict([]byte(s), &v{}); err == nil {
			t.Errorf("%s: expected an error", s)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "dir")
	path := filepath.Join(dir, "f.json")
	if err := writeFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, err %v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", st.Mode().Perm())
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
		}
	}
}

func TestWriteFileAtomicRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("evil"), 0o600); err == nil {
		t.Fatal("expected refusal to replace a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("symlink target was modified: %q", b)
	}
	dirLink := filepath.Join(root, "dirlink")
	if err := os.Symlink(root, dirLink); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dirLink); err == nil {
		t.Fatal("expected ensurePrivateDir to refuse a symlinked dir")
	}
}
