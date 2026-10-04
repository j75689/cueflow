// Package secret encrypts connector credentials at rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
)

// Box seals and opens values with a single AES-256-GCM key. The ciphertext layout is
// nonce || sealed, and the associated data binds a ciphertext to the row it belongs to,
// so a secret blob copied onto another connector row fails to decrypt.
type Box struct {
	aead cipher.AEAD
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("secret: ciphertext too short")
	}
	out, err := b.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
	if err != nil {
		return nil, errors.New("secret: decryption failed (wrong CUEFLOW_SECRET_KEY?)")
	}
	return out, nil
}

// SealMap encrypts a map of secret fields. A nil or empty map yields nil.
func (b *Box) SealMap(m map[string]any, aad []byte) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return b.Seal(raw, aad)
}

func (b *Box) OpenMap(ciphertext, aad []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(ciphertext) == 0 {
		return m, nil
	}
	raw, err := b.Open(ciphertext, aad)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
