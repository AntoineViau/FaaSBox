package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
)

// encryptionKey holds the decoded AES-256 key. Nil if not configured.
var encryptionKey []byte

// initEncryptionKey reads and validates the FAASBOX_ENCRYPTION_KEY environment variable.
// The key must be a 64-character hex string (32 bytes for AES-256).
// If the variable is absent, encryption is disabled and a warning is logged.
func initEncryptionKey() {
	hexKey := os.Getenv("FAASBOX_ENCRYPTION_KEY")
	if hexKey == "" {
		log.Println("WARNING: FAASBOX_ENCRYPTION_KEY not set — encrypted environment variables are disabled")
		return
	}

	key, err := hex.DecodeString(hexKey)
	if err != nil {
		log.Fatalf("FAASBOX_ENCRYPTION_KEY is not valid hex: %v", err)
	}
	if len(key) != 32 {
		log.Fatalf("FAASBOX_ENCRYPTION_KEY must be 64 hex characters (32 bytes for AES-256), got %d bytes", len(key))
	}

	encryptionKey = key
}

// encrypt encrypts plaintext using AES-256-GCM and returns a base64-encoded string
// containing the nonce prepended to the ciphertext.
func encrypt(plaintext, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decodes a base64 string and decrypts it using AES-256-GCM.
// The input format is base64(nonce || ciphertext).
func decrypt(encoded string, key []byte) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm.Open: %w", err)
	}

	return plaintext, nil
}
