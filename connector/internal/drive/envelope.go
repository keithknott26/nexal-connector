// Package drive is the connector's client for neXal Drive.
//
// It encrypts object bodies on this Mac before they leave it, uploads them to
// the coordinator, and decrypts them on the way back. The coordinator stores
// ciphertext and never holds a key that can open it.
//
// # Why this exists separately from internal/bundletransfer
//
// bundletransfer implements exactly this chain -- ML-KEM-768 encapsulation to a
// recipient's public key, HKDF-SHA256 to an AES-256-GCM key, AEAD over the
// plaintext -- but it is built for a different job: a short, single-use transfer
// to ANOTHER host, bound to a coordinator-issued transfer record, capped at
// MaxPlaintext (6000 bytes). None of that fits a drive object, which is up to
// 25 MiB, is addressed by a stable user-chosen key rather than a transfer id,
// and is encrypted TO THIS MACHINE for later retrieval rather than to a peer.
//
// The cryptographic construction is deliberately the same, so there is one
// chain to review rather than two.
//
// # Threat model, stated honestly
//
// What this protects: the coordinator, the R2 bucket, anyone who obtains the
// stored bytes, and anyone who records the connection, cannot read object
// contents. That is the harvest-now-decrypt-later case: ciphertext captured
// today is still unreadable when RSA and ECC fall, because the object key was
// never protected by either.
//
// What this does NOT protect: the object KEY (its path) and its SIZE are
// visible to the coordinator by construction -- the coordinator has to index
// keys and meter sizes to enforce a quota. Anyone who holds this Mac's
// decapsulation key file can read every object. There is no sharing, no
// multi-recipient access, and no key rotation in this version; each is a
// separate piece of work and none is quietly half-implemented here.
package drive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const (
	// MaxObjectBytes mirrors MAX_DRIVE_OBJECT_BYTES in the coordinator's
	// src/types.ts (25 MiB). It bounds the CIPHERTEXT, because that is what the
	// coordinator measures and what its 413 refers to.
	MaxObjectBytes = 25 * 1024 * 1024

	// Overhead is what sealing adds: the ML-KEM-768 ciphertext, the GCM nonce and
	// the GCM tag. Kept as one constant so MaxPlaintextBytes cannot drift from the
	// wire format.
	Overhead = mlkem.CiphertextSize768 + nonceSize + tagSize

	// MaxPlaintextBytes is the largest object body a caller may seal. Exported so
	// a caller can refuse an oversized file before spending time encrypting it,
	// and so the CLI's error can name the real limit rather than the server's.
	MaxPlaintextBytes = MaxObjectBytes - Overhead

	nonceSize = 12
	tagSize   = 16

	// envelopeInfo domain-separates this HKDF use from every other HKDF use in
	// the connector. Two different purposes must never derive the same key from
	// the same shared secret, and a version marker means a future format change
	// cannot be confused with this one by a decrypting client.
	envelopeInfo = "nexal-drive-object-v1"
)

// ErrPlaintextTooLarge is returned by Seal rather than a generic error so a
// caller can distinguish "this file is too big" from "encryption failed",
// which are different problems for the person running the command.
var ErrPlaintextTooLarge = errors.New("drive: object exceeds the per-object size limit")

// aead derives the content key and returns the AEAD.
//
// The associated data binds the ciphertext to the drive key it was stored
// under. Without it, an attacker who can write to the bucket could move a
// ciphertext from one drive key to another and a later Open would succeed while
// returning the wrong file's contents -- silent, and exactly the kind of
// corruption a backup must not have. With it, such a swap fails authentication.
func aead(secret []byte, driveKey string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, envelopeInfo+"\n"+driveKey, 32)
	if err != nil {
		return nil, errors.New("drive: cannot derive content key")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("drive: cannot derive content key")
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext to public, returning the wire format:
//
//	ML-KEM-768 ciphertext (1088) || GCM nonce (12) || GCM ciphertext+tag
//
// The caller keeps ownership of plaintext and is responsible for clearing it;
// Seal does not clear a buffer it did not allocate, because the caller may
// still need it (for example to report a size) and a surprise zeroed slice is
// worse than an explicit one.
func Seal(public *mlkem.EncapsulationKey768, driveKey string, plaintext []byte) ([]byte, error) {
	if public == nil {
		return nil, errors.New("drive: no encryption key")
	}
	if err := ValidateKey(driveKey); err != nil {
		return nil, err
	}
	if len(plaintext) > MaxPlaintextBytes {
		return nil, fmt.Errorf("%w: %d bytes of content exceeds the %d-byte limit (%d bytes of the %d-byte object budget are encryption overhead)",
			ErrPlaintextTooLarge, len(plaintext), MaxPlaintextBytes, Overhead, MaxObjectBytes)
	}
	secret, kem := public.Encapsulate()
	defer clear(secret)
	gcm, err := aead(secret, driveKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("drive: no randomness available")
	}
	// One allocation of the exact final size. append-of-append would reallocate
	// and leave a copy of part of the ciphertext in a dead buffer.
	out := make([]byte, 0, len(kem)+len(nonce)+len(plaintext)+tagSize)
	out = append(out, kem...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, []byte(driveKey)), nil
}

// Open reverses Seal. A ciphertext that was stored under a different drive key,
// truncated, or altered in any byte fails authentication and returns an error;
// it never returns partial or unauthenticated plaintext.
func Open(private *mlkem.DecapsulationKey768, driveKey string, wire []byte) ([]byte, error) {
	if private == nil {
		return nil, errors.New("drive: no decryption key")
	}
	if err := ValidateKey(driveKey); err != nil {
		return nil, err
	}
	if len(wire) < Overhead {
		// Deliberately not "corrupt": a short body is far more often a truncated
		// download than a malicious one, and naming the likely cause is more
		// useful to the person holding the terminal.
		return nil, errors.New("drive: stored object is shorter than one envelope; the download may be incomplete")
	}
	kem := wire[:mlkem.CiphertextSize768]
	nonce := wire[mlkem.CiphertextSize768 : mlkem.CiphertextSize768+nonceSize]
	body := wire[mlkem.CiphertextSize768+nonceSize:]
	secret, err := private.Decapsulate(kem)
	if err != nil {
		return nil, errors.New("drive: this object was not encrypted to this Mac's key")
	}
	defer clear(secret)
	gcm, err := aead(secret, driveKey)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body, []byte(driveKey))
	if err != nil {
		// ML-KEM decapsulation is implicit-rejection: a wrong key yields a
		// well-formed but wrong shared secret rather than an error, so the real
		// "wrong key" signal arrives HERE, as an AEAD failure, and is
		// indistinguishable from tampering. Say both rather than guess.
		return nil, errors.New("drive: object failed authentication; it was encrypted to a different key, stored under a different name, or altered")
	}
	return plain, nil
}
