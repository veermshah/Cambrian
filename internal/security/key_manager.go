// Package security provides three primitives the rest of the system uses
// without thinking: AES-256-GCM encrypt/decrypt for keys at rest, a zap
// field redactor that prevents secrets from leaking into logs, and a tx
// validator that runs a SimResult + slippage + allowlist gate before any
// mainnet broadcast. Spec lines 497-501.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

// MasterKeyEnv is the environment variable the bootstrap path reads to
// resolve the wallet-encryption master key. Tests override via
// LoadMasterKey(envName).
const MasterKeyEnv = "MASTER_ENCRYPTION_KEY"

const (
	// MasterKeySize is 32 bytes — AES-256 keys.
	MasterKeySize = 32
	// nonceSize is 12 — the standard GCM nonce length. Stored prepended
	// to the ciphertext so Decrypt is parameterless beyond the master key.
	nonceSize = 12
)

// Encrypt wraps plaintext under AES-256-GCM with a fresh random nonce.
// Output layout: nonce (12B) || ciphertext || GCM tag (16B). The
// chain.Wallet.KeyEncrypted column expects this format.
func Encrypt(plaintext, masterKey []byte) ([]byte, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("security: master key must be %d bytes, got %d", MasterKeySize, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("security: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("security: gcm init: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("security: nonce: %w", err)
	}
	out := make([]byte, 0, nonceSize+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt unwraps a blob produced by Encrypt. Tampered ciphertext
// (any bit flipped in nonce, ciphertext, or tag) returns an error from
// gcm.Open — verified by the AES tamper test.
func Decrypt(ciphertext, masterKey []byte) ([]byte, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("security: master key must be %d bytes, got %d", MasterKeySize, len(masterKey))
	}
	if len(ciphertext) < nonceSize {
		return nil, errors.New("security: ciphertext too short")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("security: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("security: gcm init: %w", err)
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("security: open: %w", err)
	}
	return pt, nil
}

// LoadMasterKey reads the master key from the named env var and validates
// the length. Bootstrap calls this once at startup; subsequent callers
// pass the byte slice around explicitly so a missing key fails loudly
// rather than silently substituting empty bytes.
func LoadMasterKey(envName string) ([]byte, error) {
	if envName == "" {
		envName = MasterKeyEnv
	}
	raw := os.Getenv(envName)
	if raw == "" {
		return nil, fmt.Errorf("security: %s not set", envName)
	}
	if len(raw) != MasterKeySize {
		return nil, fmt.Errorf("security: %s must be %d bytes, got %d", envName, MasterKeySize, len(raw))
	}
	out := make([]byte, MasterKeySize)
	copy(out, raw)
	return out, nil
}
