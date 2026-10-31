package remoteinference

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// SharedGPU is a controlling Mac's GPU tier, handed to the Mac serving a model when that Mac has none of its own:
// where to call, and the API key sealed to the serving Mac's X25519 public key. The coordinator relays it but cannot
// open it; the serving Mac keeps the opened key in memory only.
type SharedGPU struct {
	Name             string `json:"name"`
	BaseURL          string `json:"baseUrl"`
	Model            string `json:"model"`
	FirstByteSeconds int    `json:"firstByteSeconds"`
	SenderKey        string `json:"senderKey"` // the sender's one-time X25519 public key, base64
	SealedKey        string `json:"sealedKey"` // nonce || AES-256-GCM(API key), base64
}

const sealContext = "nexal-offload-key-v1"

var b64 = base64.StdEncoding

func sealKey(shared, sender, recipient []byte) []byte {
	h := sha256.New()
	h.Write([]byte(sealContext))
	h.Write(shared)
	h.Write(sender)
	h.Write(recipient)
	return h.Sum(nil)
}

// Seal encrypts secret to the recipient's X25519 public key (base64) with a fresh sender key.
func Seal(recipientPub, secret string) (senderPub, sealed string, err error) {
	raw, err := b64.DecodeString(recipientPub)
	if err != nil {
		return "", "", errors.New("invalid recipient key")
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return "", "", errors.New("invalid recipient key")
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", "", errors.New("invalid recipient key")
	}
	gcm, err := newGCM(sealKey(shared, eph.PublicKey().Bytes(), raw))
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(secret), []byte(sealContext))
	return b64.EncodeToString(eph.PublicKey().Bytes()), b64.EncodeToString(out), nil
}

// Open decrypts what Seal produced for priv.
func Open(priv *ecdh.PrivateKey, senderPub, sealed string) (string, error) {
	sp, err1 := b64.DecodeString(senderPub)
	box, err2 := b64.DecodeString(sealed)
	if err1 != nil || err2 != nil || priv == nil {
		return "", errors.New("unreadable sealed key")
	}
	pub, err := ecdh.X25519().NewPublicKey(sp)
	if err != nil {
		return "", errors.New("unreadable sealed key")
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return "", errors.New("unreadable sealed key")
	}
	gcm, err := newGCM(sealKey(shared, sp, priv.PublicKey().Bytes()))
	if err != nil {
		return "", err
	}
	if len(box) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("unreadable sealed key")
	}
	plain, err := gcm.Open(nil, box[:gcm.NonceSize()], box[gcm.NonceSize():], []byte(sealContext))
	if err != nil {
		return "", errors.New("the sealed key is not for this Mac")
	}
	return string(plain), nil
}

// PublicKey is priv's public half, base64, as the serving Mac publishes it.
func PublicKey(priv *ecdh.PrivateKey) string { return b64.EncodeToString(priv.PublicKey().Bytes()) }

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
