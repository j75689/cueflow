package secret

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t)
	ct, err := b.SealMap(map[string]any{"api_key": "sk-secret"}, []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("sk-secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	m, err := b.OpenMap(ct, []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	if m["api_key"] != "sk-secret" {
		t.Fatalf("got %v", m)
	}
}

func TestAssociatedDataBindsRow(t *testing.T) {
	b := newBox(t)
	ct, _ := b.SealMap(map[string]any{"password": "x"}, []byte("row-1"))
	if _, err := b.OpenMap(ct, []byte("row-2")); err == nil {
		t.Fatal("ciphertext moved to another row must not decrypt")
	}
}

func TestWrongKeyFails(t *testing.T) {
	ct, _ := newBox(t).Seal([]byte("hello"), nil)
	if _, err := newBox(t).Open(ct, nil); err == nil {
		t.Fatal("expected failure with a different key")
	}
}

func TestNonceIsRandom(t *testing.T) {
	b := newBox(t)
	a, _ := b.Seal([]byte("same"), nil)
	c, _ := b.Seal([]byte("same"), nil)
	if bytes.Equal(a, c) {
		t.Fatal("two seals of the same plaintext must differ")
	}
}

func TestEmpty(t *testing.T) {
	b := newBox(t)
	ct, err := b.SealMap(nil, nil)
	if err != nil || ct != nil {
		t.Fatalf("got %v %v", ct, err)
	}
	m, err := b.OpenMap(nil, nil)
	if err != nil || len(m) != 0 {
		t.Fatalf("got %v %v", m, err)
	}
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("short key should fail")
	}
}
