package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"unicode/utf8"
)

// What the instance encrypts at rest, and the two rules that keep it from
// eating itself: a stored value announces that it is encrypted, and an empty
// value is never encrypted at all.
//
// FAASBOX_ENCRYPTION_KEY is the single input secret. Nothing uses it directly:
// every purpose derives its own subkey through HKDF under a label of its own,
// so a subkey published in the clear — a blind index, say — says nothing about
// the one that encrypts.

const (
	// encryptionKeyEnv names the only source of the master key.
	encryptionKeyEnv = "FAASBOX_ENCRYPTION_KEY"

	// hkdfInfoCipher labels the subkey that encrypts stored values. It is never
	// reused for anything else: HKDF under a different label yields an
	// independent key, which is what lets another purpose take one without
	// touching this one.
	hkdfInfoCipher = "faasbox/v1/cipher"

	// hkdfInfoIndex labels the subkey the blind indexes are computed with.
	//
	// The separation is not cosmetic. A blind index is stored **in the clear**,
	// next to the value it fingerprints, and it is the one output of this scheme
	// an attacker reads for free. It must say nothing about the key that encrypts
	// the rest, and under HKDF a different label is what guarantees that.
	hkdfInfoIndex = "faasbox/v1/blind-index"

	// cipherPrefix marks a stored value as already encrypted.
	//
	// The fields concerned are written *in place* — the caller sends `script`,
	// the hook encrypts `script` — so a hook that encrypted unconditionally
	// would stack a layer at every save. The prefix is read off the value
	// itself and depends on no context: record.Original() would not do, since a
	// partial save, a direct SQL write or a reloaded record all give cases
	// where the original is not the one we think.
	//
	// It carries a version so a change of algorithm stays possible without
	// having to guess.
	cipherPrefix = "fbx1:"

	// cipherEnvelope is what AES-256-GCM adds around a plaintext: the nonce it
	// prepends and the authentication tag it appends.
	cipherEnvelope = 12 + 16
)

// masterKey holds the decoded FAASBOX_ENCRYPTION_KEY. It is the input of every
// derivation and is never used to encrypt anything itself.
var masterKey []byte

// cipherKey is the subkey every stored value is encrypted with.
var cipherKey []byte

// indexKey is the subkey every blind index is computed with. It encrypts
// nothing, and nothing encrypted is ever computed with it.
var indexKey []byte

// initEncryptionKey reads and validates FAASBOX_ENCRYPTION_KEY, then derives the
// subkey the stored values are encrypted with. The variable must be a
// 64-character hex string (32 bytes for AES-256).
//
// Its absence is fatal, exactly like an invalid value. There is no degraded mode
// left to fall back on: the code of a function, its triggers and its execution
// history are all encrypted at rest, so an instance without the key can neither
// write them nor read them back. Starting anyway would mean serving base64 to
// the editor and calling it a running server.
func initEncryptionKey() {
	hexKey := os.Getenv(encryptionKeyEnv)
	if hexKey == "" {
		log.Fatalf("%s is not set. It is required: the database is encrypted at rest, "+
			"and an instance without the key can neither write nor read its own content.",
			encryptionKeyEnv)
	}

	key, err := hex.DecodeString(hexKey)
	if err != nil {
		log.Fatalf("%s is not valid hex: %v", encryptionKeyEnv, err)
	}
	if len(key) != 32 {
		log.Fatalf("%s must be 64 hex characters (32 bytes for AES-256), got %d bytes",
			encryptionKeyEnv, len(key))
	}

	masterKey = key
	derived, err := deriveKey(key, hkdfInfoCipher)
	if err != nil {
		log.Fatalf("failed to derive the encryption subkey from %s: %v", encryptionKeyEnv, err)
	}
	cipherKey = derived

	index, err := deriveKey(key, hkdfInfoIndex)
	if err != nil {
		log.Fatalf("failed to derive the blind index subkey from %s: %v", encryptionKeyEnv, err)
	}
	indexKey = index
}

// deriveKey expands the master key into a 32-byte subkey for one purpose.
//
// The salt is empty on purpose: HKDF tolerates it, the master key is already
// uniformly random, and a salt stored beside the ciphertext would buy nothing
// here. What separates two subkeys is the label, and nothing else.
func deriveKey(master []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, master, nil, info, 32)
}

// blindIndex returns an indexable fingerprint of a value, computed with a
// subkey of its own.
//
// **It is an HMAC and not a bare SHA-256**, and the reason is the one thing to
// retain here: the space of function names is small and guessable — "webhook",
// "send-email", "daily-report". A bare hash of one would fall to a dictionary in
// seconds from a dump, which would hand back in the index exactly what
// encrypting the column took away. The HMAC cannot, because the subkey is not in
// the database: without it no candidate is computable at all.
//
// That property rests on a single fact — **the key does not travel with the
// backup**. The day it does, the index is guessable again, in seconds. It is
// what makes an index in the clear acceptable, not an implementation detail.
//
// The fingerprint is taken on the exact value, with no normalisation of any
// kind: validName accepts capitals, so `Alpha` and `alpha` are two names, and
// two fingerprints. An empty value has none — the same rule the cipher follows,
// and the columns carrying one are required anyway.
func blindIndex(value string) string {
	if value == "" || indexKey == nil {
		return ""
	}
	mac := hmac.New(sha256.New, indexKey)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// alreadySealed reports whether a value is one this code produced.
//
// **The prefix is verified, not believed**, and that is the whole point of this
// function. A plaintext that happens to start with `fbx1:` — the output of a
// function, the payload of a trigger, a script someone pasted — would otherwise
// be taken for a ciphertext and stored **in the clear**, which is the one
// property this scheme exists to establish. Worse on a source column: nothing
// would open it on the way back, the accessor would yield the empty string, and
// syncRecordToDisk would remove the file.
//
// So a value counts as sealed only if it actually opens. The cost is one AES
// open per already-sealed field and per save, which is nothing at this scale,
// and the stored format does not move: the prefix stays the signal.
//
// The read side keeps the looser test on purpose (see decryptField). There, a
// value carrying the prefix that will not open is not a plaintext to hand back —
// it is a value written under another key, and relaying it would publish a
// ciphertext as content.
func alreadySealed(value string) bool {
	if cipherKey == nil || !strings.HasPrefix(value, cipherPrefix) {
		return false
	}
	_, err := decrypt(strings.TrimPrefix(value, cipherPrefix), cipherKey)
	return err == nil
}

// encryptField returns the stored form of a value.
//
// Two values come back untouched, and both are rules rather than shortcuts.
// A value that is already sealed must not be sealed again, or a second save
// would stack a layer. And **an empty value stays empty**: encrypting "" would
// produce something non-empty, and three rules of the product read an empty
// field as a fact — a cleared script removes its file, an absent value is not an
// empty one, and an empty depsError means no error. Nothing is lost by it, the
// presence of a value being part of the metadata this scheme leaves in the clear
// anyway.
func encryptField(plaintext string) (string, error) {
	if plaintext == "" {
		return plaintext, nil
	}
	// Checked before the sealed test, which cannot answer without the key:
	// without one, "already sealed?" has no answer rather than a false one.
	if cipherKey == nil {
		return "", fmt.Errorf("%s is not configured", encryptionKeyEnv)
	}
	if alreadySealed(plaintext) {
		return plaintext, nil
	}

	sealed, err := encrypt([]byte(plaintext), cipherKey)
	if err != nil {
		return "", err
	}
	return cipherPrefix + sealed, nil
}

// decryptField returns the plaintext of a stored value.
//
// A value without the prefix was never encrypted and comes back as it stands:
// that is what makes the scheme readable on a record written before the hooks
// were in place, and what keeps the in-memory records built by the OAuth bearer
// path — which nothing ever encrypted — legible through the same accessors.
//
// The test here is the prefix alone, deliberately looser than alreadySealed. A
// stored value carrying it that will not open was written under another key or
// is corrupt; that is an error to report, not a plaintext to relay. Handing it
// back would publish a ciphertext as content — silently, on the two JSON columns
// where it has exactly the shape a truncated payload legitimately takes.
func decryptField(stored string) (string, error) {
	if !strings.HasPrefix(stored, cipherPrefix) {
		return stored, nil
	}
	if cipherKey == nil {
		return "", fmt.Errorf("%s is not configured", encryptionKeyEnv)
	}

	plaintext, err := decrypt(strings.TrimPrefix(stored, cipherPrefix), cipherKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// cipherMax is the declared column size a plaintext cap of n *runes* needs once
// encrypted.
//
// A PocketBase TextField measures the value it stores, and the stored value is
// base64 of the nonce, the ciphertext and the tag — about a third larger than
// the plaintext bytes. The cap is counted in runes while the cipher works on
// bytes, so the worst case is taken: four bytes per rune. The column is
// therefore never the binding constraint, which is deliberate — the limit the
// product announces is counted on the plaintext and checked before encrypting.
func cipherMax(runes int) int {
	return len(cipherPrefix) + base64.StdEncoding.EncodedLen(utf8.UTFMax*runes+cipherEnvelope)
}

// cipherMaxBytes is cipherMax for a bound already expressed in bytes. The two
// differ only by the rune-to-byte majoration: cipherMax has to assume the worst
// case of utf8.UTFMax bytes per rune, this one has the byte count already.
//
// A JSONField declares its size in bytes, where a TextField counts runes, so
// which of the two applies is read off the field type at the call site.
func cipherMaxBytes(n int) int {
	return len(cipherPrefix) + base64.StdEncoding.EncodedLen(n+cipherEnvelope)
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
