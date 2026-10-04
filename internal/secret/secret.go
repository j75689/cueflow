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

// Box seals values with a primary AES-256-GCM key and opens them with the primary key or any
// previous key (for zero-downtime key rotation). The ciphertext layout is nonce || sealed, and the
// associated data binds a ciphertext to the row it belongs to, so a secret blob copied onto another
// connector row fails to decrypt.
type Box struct {
	aead     cipher.AEAD   // primary: used to seal
	previous []cipher.AEAD // also tried when opening
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// New builds a Box from the primary key and optional previous keys.
func New(key []byte, previous ...[]byte) (*Box, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	b := &Box{aead: aead}
	for _, k := range previous {
		p, err := newAEAD(k)
		if err != nil {
			return nil, fmt.Errorf("previous %w", err)
		}
		b.previous = append(b.previous, p)
	}
	return b, nil
}

// HasPrevious reports whether previous keys are configured.
func (b *Box) HasPrevious() bool { return len(b.previous) > 0 }

func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	out, _, err := b.OpenWithKeyIndex(ciphertext, aad)
	return out, err
}

// OpenWithKeyIndex decrypts and reports which key worked: 0 = primary, 1.. = previous keys.
func (b *Box) OpenWithKeyIndex(ciphertext, aad []byte) ([]byte, int, error) {
	ns := b.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, -1, errors.New("secret: ciphertext too short")
	}
	for i, a := range append([]cipher.AEAD{b.aead}, b.previous...) {
		if out, err := a.Open(nil, ciphertext[:ns], ciphertext[ns:], aad); err == nil {
			return out, i, nil
		}
	}
	return nil, -1, errors.New("secret: decryption failed (wrong CUEFLOW_SECRET_KEY?)")
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
