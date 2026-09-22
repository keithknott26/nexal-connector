package drive

import (
	"bytes"
	"crypto/mlkem"
	"strings"
	"testing"
)

func testKey(t *testing.T) *mlkem.DecapsulationKey768 {
	t.Helper()
	key, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatalf("GenerateKey768: %v", err)
	}
	return key
}

func TestSealedObjectRoundTrips(t *testing.T) {
	key := testKey(t)
	plain := []byte("the contents of a backup band")
	sealed, err := Seal(PublicKey(key), "backups/2026/band.bin", plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("the plaintext appears verbatim in the sealed object")
	}
	opened, err := Open(key, "backups/2026/band.bin", sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("round trip returned %q, want %q", opened, plain)
	}
}

// The property that makes the drive key load-bearing rather than decorative.
func TestAnotherMacsKeyCannotOpenTheObject(t *testing.T) {
	mine, theirs := testKey(t), testKey(t)
	sealed, err := Seal(PublicKey(mine), "private.txt", []byte("mine"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(theirs, "private.txt", sealed); err == nil {
		t.Fatal("an unrelated Mac's key opened the object")
	}
}

// The reason the drive key is the AEAD associated data. Without this binding an
// attacker with write access to the bucket could rename one object over another
// and a later restore would silently return the wrong file.
func TestObjectMovedToAnotherKeyFailsAuthentication(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(PublicKey(key), "documents/tax-return.pdf", []byte("sensitive"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(key, "documents/shopping-list.txt", sealed); err == nil {
		t.Fatal("an object stored under a different key opened without error")
	}
}

func TestTamperedObjectIsRefusedAtEveryOffset(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(PublicKey(key), "o", bytes.Repeat([]byte("a"), 64))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// One byte in each region: the KEM ciphertext, the nonce, the body, the tag.
	for _, offset := range []int{0, mlkem.CiphertextSize768 + 1, mlkem.CiphertextSize768 + nonceSize + 1, len(sealed) - 1} {
		altered := bytes.Clone(sealed)
		altered[offset] ^= 0x01
		if _, err := Open(key, "o", altered); err == nil {
			t.Fatalf("a single flipped bit at offset %d was accepted", offset)
		}
	}
}

func TestTruncatedObjectIsRefusedRatherThanPartiallyDecrypted(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(PublicKey(key), "o", bytes.Repeat([]byte("a"), 4096))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for _, size := range []int{0, 1, Overhead - 1, Overhead, len(sealed) / 2, len(sealed) - 1} {
		if _, err := Open(key, "o", sealed[:size]); err == nil {
			t.Fatalf("a body truncated to %d bytes was accepted", size)
		}
	}
}

// Encryption is randomised, so the same file uploaded twice must not produce
// identical ciphertext -- otherwise the coordinator could tell that two objects
// have the same contents, which is exactly the leak encryption is meant to stop.
func TestTheSameContentSealsDifferentlyEachTime(t *testing.T) {
	key := testKey(t)
	first, err := Seal(PublicKey(key), "o", []byte("identical"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := Seal(PublicKey(key), "o", []byte("identical"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("sealing the same content twice produced identical bytes")
	}
}

func TestSizeLimitIsEnforcedAgainstTheCiphertextBudget(t *testing.T) {
	key := testKey(t)
	atLimit, err := Seal(PublicKey(key), "o", make([]byte, MaxPlaintextBytes))
	if err != nil {
		t.Fatalf("a plaintext at exactly the limit was refused: %v", err)
	}
	if len(atLimit) != MaxObjectBytes {
		t.Fatalf("a maximum plaintext sealed to %d bytes, want exactly %d", len(atLimit), MaxObjectBytes)
	}
	_, err = Seal(PublicKey(key), "o", make([]byte, MaxPlaintextBytes+1))
	if err == nil {
		t.Fatal("a plaintext one byte over the limit was accepted")
	}
	if !strings.Contains(err.Error(), "overhead") {
		t.Errorf("the size error does not explain the overhead: %v", err)
	}
}

func TestEmptyObjectIsLegal(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(PublicKey(key), "empty", nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := Open(key, "empty", sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("an empty object opened to %d bytes", len(opened))
	}
}

func TestSealAndOpenRefuseAnInvalidKeyBeforeDoingAnyWork(t *testing.T) {
	key := testKey(t)
	for _, bad := range []string{"", "/leading", "trailing/", "a//b", `back\slash`, "a/../b", "a/./b", "nul\x00byte"} {
		if _, err := Seal(PublicKey(key), bad, []byte("x")); err == nil {
			t.Errorf("Seal accepted invalid key %q", bad)
		}
		if _, err := Open(key, bad, make([]byte, Overhead+16)); err == nil {
			t.Errorf("Open accepted invalid key %q", bad)
		}
	}
}

func TestNilKeysAreRefusedRatherThanPanicking(t *testing.T) {
	if _, err := Seal(nil, "o", []byte("x")); err == nil {
		t.Error("Seal accepted a nil public key")
	}
	if _, err := Open(nil, "o", make([]byte, Overhead)); err == nil {
		t.Error("Open accepted a nil private key")
	}
}
