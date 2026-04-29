package chain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/gagliardetto/solana-go"
)

// Spec lines 363-366 specify Ed25519 wallets for Solana. The plaintext
// private key never leaves this file in production: chunk 17 will move
// the encrypt / decrypt path behind the security package. Until that
// chunk lands, callers pass a 32-byte master key and we use AES-256-GCM
// directly.

const (
	masterKeySize = 32
	gcmNonceSize  = 12
)

// NewSolanaWallet generates a fresh Ed25519 keypair, encrypts the seed
// with the given 32-byte master key under AES-256-GCM, and returns a
// chain.Wallet whose Address is the canonical base58 public key.
func NewSolanaWallet(masterKey []byte) (*Wallet, error) {
	if len(masterKey) != masterKeySize {
		return nil, fmt.Errorf("solana wallet: master key must be %d bytes, got %d", masterKeySize, len(masterKey))
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: keygen: %w", err)
	}
	enc, err := encryptSeed(masterKey, priv.Seed())
	if err != nil {
		return nil, err
	}
	return &Wallet{
		Chain:        "solana",
		Address:      solana.PublicKeyFromBytes(pub).String(),
		KeyEncrypted: enc,
	}, nil
}

// SolanaWalletFromSeed wraps an existing Ed25519 seed (32 bytes) into an
// encrypted Wallet. Used by the devnet integration test that loads a
// pre-funded keypair from the environment.
func SolanaWalletFromSeed(masterKey, seed []byte) (*Wallet, error) {
	if len(masterKey) != masterKeySize {
		return nil, fmt.Errorf("solana wallet: master key must be %d bytes, got %d", masterKeySize, len(masterKey))
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("solana wallet: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("solana wallet: failed to derive public key")
	}
	enc, err := encryptSeed(masterKey, seed)
	if err != nil {
		return nil, err
	}
	return &Wallet{
		Chain:        "solana",
		Address:      solana.PublicKeyFromBytes(pub).String(),
		KeyEncrypted: enc,
	}, nil
}

// UnsealSolanaWallet returns the live solana-go PrivateKey for a Wallet.
// Callers must zero the returned key when done; in this codebase only
// the SolanaClient swap path holds it briefly for signing.
func UnsealSolanaWallet(masterKey []byte, w *Wallet) (solana.PrivateKey, error) {
	if w == nil {
		return nil, errors.New("solana wallet: nil wallet")
	}
	if w.Chain != "solana" {
		return nil, fmt.Errorf("solana wallet: chain mismatch: %q", w.Chain)
	}
	seed, err := decryptSeed(masterKey, w.KeyEncrypted)
	if err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return solana.PrivateKey(priv), nil
}

// encryptSeed: AES-256-GCM(seed) with a random 12-byte nonce. Storage
// layout: nonce || ciphertext+tag. The nonce length is fixed so the
// decrypt path can split without a length prefix.
func encryptSeed(masterKey, seed []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: gcm: %w", err)
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("solana wallet: nonce: %w", err)
	}
	out := make([]byte, 0, gcmNonceSize+gcm.Overhead()+len(seed))
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, seed, nil), nil
}

func decryptSeed(masterKey, blob []byte) ([]byte, error) {
	if len(blob) < gcmNonceSize {
		return nil, errors.New("solana wallet: ciphertext too short")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: gcm: %w", err)
	}
	nonce, ct := blob[:gcmNonceSize], blob[gcmNonceSize:]
	seed, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("solana wallet: open: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("solana wallet: decrypted seed has wrong size %d", len(seed))
	}
	return seed, nil
}
