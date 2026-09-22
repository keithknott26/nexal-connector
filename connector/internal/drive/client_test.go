package drive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// privateDir returns a 0700 directory. config.AtomicPrivate requires the parent
// to be 0700 and t.TempDir() is not guaranteed to be.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	return dir
}

// fakeCoordinator stands in for the Worker. It enforces the parts of the real
// contract this client depends on -- bearer auth, the SHA-256 header, the size
// bound -- so a client change that violated them would fail here.
type fakeCoordinator struct {
	objects map[string][]byte
	paths   []string
	auth    []string
}

func (f *fakeCoordinator) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.URL.RequestURI())
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer host-token" {
			refuse(w, http.StatusUnauthorized, "unauthorized", "bad credential")
			return
		}
		key, ok := strings.CutPrefix(r.URL.Path, "/api/drive/objects/")
		if !ok {
			refuse(w, http.StatusNotFound, "not_found", "no such route")
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxObjectBytes+1))
			if err != nil || len(body) > MaxObjectBytes {
				refuse(w, http.StatusRequestEntityTooLarge, "drive_object_too_large", "too large")
				return
			}
			sum := sha256.Sum256(body)
			if r.Header.Get("X-Nexal-Content-Sha256") != hex.EncodeToString(sum[:]) {
				refuse(w, http.StatusBadRequest, "drive_object_hash_mismatch", "digest does not match")
				return
			}
			f.objects[key] = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"id": "dobj_1", "key": key, "sizeBytes": len(body), "createdAt": "2026-09-22T00:00:00Z"},
			})
		case http.MethodGet:
			body, ok := f.objects[key]
			if !ok {
				refuse(w, http.StatusNotFound, "drive_object_not_found", "no such object")
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(body)
		default:
			refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "no")
		}
	})
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func newFake(t *testing.T) (*Client, *fakeCoordinator) {
	t.Helper()
	fake := &fakeCoordinator{objects: map[string][]byte{}}
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	// dev=true so a plain-HTTP test origin passes ValidateURL, exactly as the
	// other connector packages test against httptest.
	client, err := NewClient(server.URL, "host-token", true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.http = server.Client()
	return client, fake
}

// The whole point, end to end: bytes leave encrypted, come back, and decrypt.
func TestEncryptedObjectSurvivesAFullUploadAndDownload(t *testing.T) {
	client, fake := newFake(t)
	key := testKey(t)
	const driveKey = "backups/2026/09/mac-mini.band"
	plain := bytes.Repeat([]byte("payload"), 5000)

	sealed, err := Seal(PublicKey(key), driveKey, plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := client.Put(context.Background(), driveKey, sealed); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// What the coordinator holds must not contain the plaintext.
	for _, stored := range fake.objects {
		if bytes.Contains(stored, plain) {
			t.Fatal("the coordinator is holding the plaintext")
		}
	}

	downloaded, err := client.Get(context.Background(), driveKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	opened, err := Open(key, driveKey, downloaded)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatal("the round trip did not return the original contents")
	}
}

// Keys with '/' must reach the coordinator with separators intact, because the
// route captures the whole remainder as one key.
func TestSlashesSurviveEncodingAndOtherCharactersDoNot(t *testing.T) {
	client, fake := newFake(t)
	key := testKey(t)
	const driveKey = "a b/c+d/e?f#g/h.bin"
	sealed, err := Seal(PublicKey(key), driveKey, []byte("x"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := client.Put(context.Background(), driveKey, sealed); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := fake.paths[0]
	if strings.Count(path, "/")-strings.Count("/api/drive/objects/", "/") != strings.Count(driveKey, "/") {
		t.Errorf("separator count changed in transit: %s", path)
	}
	for _, unsafe := range []string{" ", "?", "#", "+"} {
		if strings.Contains(strings.TrimPrefix(path, "/api/drive/objects/"), unsafe) {
			t.Errorf("%q reached the wire unescaped: %s", unsafe, path)
		}
	}
	// And it round trips: the server's decoded key matches what we sealed under.
	if _, ok := fake.objects[driveKey]; !ok {
		t.Errorf("the coordinator stored a different key than the client sealed under; got %v", keysOf(fake.objects))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCoordinatorErrorCodeIsPreservedForTheCaller(t *testing.T) {
	client, _ := newFake(t)
	_, err := client.Get(context.Background(), "absent")
	var status *StatusError
	if !errorAs(err, &status) {
		t.Fatalf("Get returned %v, want a *StatusError", err)
	}
	if status.Status != http.StatusNotFound || status.Code != "drive_object_not_found" {
		t.Fatalf("got status %d code %q", status.Status, status.Code)
	}
}

// A hostile or broken coordinator must not be able to write escape sequences
// into the user's terminal through an error message.
func TestControlCharactersInACoordinatorErrorAreStripped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refuse(w, http.StatusConflict, "code\x1b[2J", "message\x1b[31mred\x00"+strings.Repeat("x", 900))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "host-token", true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.http = server.Client()
	_, err = client.Get(context.Background(), "k")
	if strings.ContainsAny(err.Error(), "\x1b\x00") {
		t.Fatalf("control characters survived into the error: %q", err.Error())
	}
	if len(err.Error()) > 600 {
		t.Fatalf("the error was not bounded: %d bytes", len(err.Error()))
	}
}

func TestBearerTokenIsSentAndNeverAppearsInAnError(t *testing.T) {
	client, fake := newFake(t)
	if _, err := client.Get(context.Background(), "absent"); err == nil {
		t.Fatal("expected a refusal")
	} else if strings.Contains(err.Error(), "host-token") {
		t.Fatal("the host credential leaked into an error message")
	}
	if fake.auth[0] != "Bearer host-token" {
		t.Fatalf("Authorization header was %q", fake.auth[0])
	}
}

func TestOversizedUploadIsRefusedBeforeItIsSent(t *testing.T) {
	client, fake := newFake(t)
	if _, err := client.Put(context.Background(), "k", make([]byte, MaxObjectBytes+1)); err == nil {
		t.Fatal("an oversized object was sent")
	}
	if len(fake.paths) != 0 {
		t.Fatal("the client contacted the coordinator before refusing")
	}
}

func TestInvalidKeysAreRefusedWithoutContactingTheCoordinator(t *testing.T) {
	client, fake := newFake(t)
	for _, bad := range []string{"", "/a", "a/", "a//b", "a/../b", `a\b`} {
		if _, err := client.Put(context.Background(), bad, []byte("x")); err == nil {
			t.Errorf("Put accepted %q", bad)
		}
		if _, err := client.Get(context.Background(), bad); err == nil {
			t.Errorf("Get accepted %q", bad)
		}
	}
	if len(fake.paths) != 0 {
		t.Fatal("an invalid key reached the coordinator")
	}
}

func TestClientRefusesAnUnusableCredentialOrOrigin(t *testing.T) {
	if _, err := NewClient("https://coordinator.example", "", false); err == nil {
		t.Error("an empty credential was accepted")
	}
	if _, err := NewClient("https://coordinator.example", "has space", false); err == nil {
		t.Error("a credential with a space was accepted")
	}
	if _, err := NewClient("http://coordinator.example", "t", false); err == nil {
		t.Error("a plain-HTTP origin was accepted outside dev mode")
	}
}

func errorAs(err error, target any) bool {
	for err != nil {
		if s, ok := err.(*StatusError); ok {
			if p, ok := target.(**StatusError); ok {
				*p = s
				return true
			}
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
