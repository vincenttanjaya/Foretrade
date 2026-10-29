package aiconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// errNoKey refuses to store a secret without SECRET_KEY; the text is shown to the admin.
var errNoKey = errors.New("SECRET_KEY is not set: add 64 hex characters (openssl rand -hex 32) to .env and restart")

// aad binds every sealed secret to this table.
var aad = []byte("ai_settings")

// box seals secrets with AES-256-GCM under SECRET_KEY: nonce‖ciphertext.
// ponytail: one key, no version prefix; add a "v1:" prefix when SECRET_KEY first rotates.
type box struct{ aead cipher.AEAD }

// newBox is nil when hexKey is not 32 bytes of hex: secrets then can't be
// saved, and stored ones can't be read.
func newBox(hexKey string) *box {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil
	}
	block, _ := aes.NewCipher(key) // a 32-byte key never fails
	aead, _ := cipher.NewGCM(block)
	return &box{aead}
}

func (b *box) seal(plain []byte) ([]byte, error) {
	if b == nil {
		return nil, errNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plain, aad), nil
}

func (b *box) open(sealed []byte) ([]byte, error) {
	if b == nil {
		return nil, errNoKey
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed secret too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], aad)
}

// hint is "…" and the last 4 characters of a secret of at least 16, else "".
func hint(secret string) string {
	if len(secret) < 16 {
		return ""
	}
	return "…" + secret[len(secret)-4:]
}
