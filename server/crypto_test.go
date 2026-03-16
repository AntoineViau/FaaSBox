package main

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func testKey() []byte {
	// 32 bytes for AES-256
	return []byte("0123456789abcdef0123456789abcdef")
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey()
	plaintext := []byte(`{"SECRET_KEY":"my-super-secret","DB_URL":"postgres://localhost"}`)

	encrypted, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if encrypted == "" {
		t.Fatal("encrypted string is empty")
	}

	// Encrypted output must be valid base64
	if _, err := base64.StdEncoding.DecodeString(encrypted); err != nil {
		t.Fatalf("encrypted output is not valid base64: %v", err)
	}

	decrypted, err := decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptProducesDifferentOutputs(t *testing.T) {
	key := testKey()
	plaintext := []byte("same input")

	a, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	b, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if a == b {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key := testKey()
	plaintext := []byte("secret data")

	encrypted, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	wrongKey := []byte("fedcba9876543210fedcba9876543210")
	_, err = decrypt(encrypted, wrongKey)
	if err == nil {
		t.Fatal("decrypt with wrong key should have failed")
	}
}

func TestDecryptCorruptedData(t *testing.T) {
	key := testKey()
	plaintext := []byte("secret data")

	encrypted, err := encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Corrupt the base64 data by flipping a character
	corrupted := []byte(encrypted)
	// Modify a character in the middle (after the nonce)
	idx := len(corrupted) / 2
	if corrupted[idx] == 'A' {
		corrupted[idx] = 'B'
	} else {
		corrupted[idx] = 'A'
	}

	_, err = decrypt(string(corrupted), key)
	if err == nil {
		t.Fatal("decrypt with corrupted data should have failed")
	}
}

func TestDecryptInvalidBase64(t *testing.T) {
	key := testKey()
	_, err := decrypt("not-valid-base64!!!", key)
	if err == nil {
		t.Fatal("decrypt with invalid base64 should have failed")
	}
}

func TestDecryptTooShort(t *testing.T) {
	key := testKey()
	// Encode something shorter than a GCM nonce (12 bytes)
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	_, err := decrypt(short, key)
	if err == nil {
		t.Fatal("decrypt with too-short data should have failed")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	key := testKey()

	encrypted, err := encrypt([]byte{}, key)
	if err != nil {
		t.Fatalf("encrypt empty plaintext failed: %v", err)
	}

	decrypted, err := decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if len(decrypted) != 0 {
		t.Fatalf("expected empty plaintext, got %q", decrypted)
	}
}
