package remoteinference

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// Open must refuse anything that is not exactly what Seal produced for this key: missing key, undecodable
// input, a truncated box, a flipped ciphertext byte, and a swapped sender key. None may panic or return text.
func TestOpenRefusesMalformedAndTamperedInput(t *testing.T) {
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	sender, sealed, err := Seal(PublicKey(priv), "rpa_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := b64.DecodeString(sealed)
	flipped := append([]byte(nil), box...)
	flipped[len(flipped)-1] ^= 0xff
	otherSender, _, _ := Seal(PublicKey(priv), "x")

	cases := map[string]struct {
		priv           *ecdh.PrivateKey
		sender, sealed string
	}{
		"nil private key":      {nil, sender, sealed},
		"bad sender base64":    {priv, "!!!", sealed},
		"bad sealed base64":    {priv, sender, "!!!"},
		"short sender key":     {priv, b64.EncodeToString([]byte("short")), sealed},
		"truncated box":        {priv, sender, b64.EncodeToString(box[:10])},
		"empty box":            {priv, sender, ""},
		"flipped ciphertext":   {priv, sender, b64.EncodeToString(flipped)},
		"different sender key": {priv, otherSender, sealed},
	}
	for name, c := range cases {
		if got, err := Open(c.priv, c.sender, c.sealed); err == nil || got != "" {
			t.Errorf("%s: want an error and no text, got %q %v", name, got, err)
		}
	}
}
