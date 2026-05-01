package chain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Spec lines 363-366: Base wallets are secp256k1 (same as any
// EVM chain). Like the Solana wallet, plaintext keys never live
// in chain.Wallet.KeyEncrypted — chunk 17 will move encrypt/decrypt
// behind internal/security. Until then we use AES-256-GCM directly
// with a 32-byte master key.

const (
	// secp256k1 private key is 32 bytes.
	baseSeedSize = 32
)

// NewBaseWallet generates a fresh secp256k1 keypair, encrypts the
// 32-byte private key with the given master key under AES-256-GCM,
// and returns a chain.Wallet whose Address is the EIP-55 checksummed
// hex string ("0x..." / 42 chars).
func NewBaseWallet(masterKey []byte) (*Wallet, error) {
	if len(masterKey) != masterKeySize {
		return nil, fmt.Errorf("base wallet: master key must be %d bytes, got %d", masterKeySize, len(masterKey))
	}
	priv, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("base wallet: keygen: %w", err)
	}
	return baseWalletFromPriv(masterKey, priv)
}

// BaseWalletFromPrivKey wraps an existing 32-byte secp256k1 private key
// into an encrypted Wallet. Used by the Sepolia integration test that
// loads a pre-funded key from the environment.
func BaseWalletFromPrivKey(masterKey, key []byte) (*Wallet, error) {
	if len(masterKey) != masterKeySize {
		return nil, fmt.Errorf("base wallet: master key must be %d bytes, got %d", masterKeySize, len(masterKey))
	}
	if len(key) != baseSeedSize {
		return nil, fmt.Errorf("base wallet: priv key must be %d bytes, got %d", baseSeedSize, len(key))
	}
	priv, err := crypto.ToECDSA(key)
	if err != nil {
		return nil, fmt.Errorf("base wallet: parse priv: %w", err)
	}
	return baseWalletFromPriv(masterKey, priv)
}

func baseWalletFromPriv(masterKey []byte, priv *ecdsa.PrivateKey) (*Wallet, error) {
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	enc, err := encryptSeed(masterKey, crypto.FromECDSA(priv))
	if err != nil {
		return nil, err
	}
	return &Wallet{
		Chain:        "base",
		Address:      addr.Hex(),
		KeyEncrypted: enc,
	}, nil
}

// UnsealBaseWallet returns the live *ecdsa.PrivateKey for a Wallet.
// Callers must zero the returned key when done — the BaseClient swap
// path holds it briefly for signing.
func UnsealBaseWallet(masterKey []byte, w *Wallet) (*ecdsa.PrivateKey, error) {
	if w == nil {
		return nil, errors.New("base wallet: nil wallet")
	}
	if w.Chain != "base" {
		return nil, fmt.Errorf("base wallet: chain mismatch: %q", w.Chain)
	}
	raw, err := decryptBaseKey(masterKey, w.KeyEncrypted)
	if err != nil {
		return nil, err
	}
	priv, err := crypto.ToECDSA(raw)
	if err != nil {
		return nil, fmt.Errorf("base wallet: rebuild priv: %w", err)
	}
	// Sanity check: derived address matches the wallet's stored address.
	if crypto.PubkeyToAddress(priv.PublicKey) != common.HexToAddress(w.Address) {
		return nil, errors.New("base wallet: decrypted key does not match address")
	}
	return priv, nil
}

// decryptBaseKey is the secp256k1-specific unwrap of decryptSeed; we
// can't reuse decryptSeed verbatim because it asserts a 32-byte ed25519
// seed length, but secp256k1 priv keys also happen to be 32 bytes.
// The byte length is identical so we share the constant masterKeySize.
func decryptBaseKey(masterKey, blob []byte) ([]byte, error) {
	if len(blob) < gcmNonceSize {
		return nil, errors.New("base wallet: ciphertext too short")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("base wallet: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("base wallet: gcm: %w", err)
	}
	nonce, ct := blob[:gcmNonceSize], blob[gcmNonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("base wallet: open: %w", err)
	}
	if len(pt) != baseSeedSize {
		return nil, fmt.Errorf("base wallet: decrypted key has wrong size %d", len(pt))
	}
	return pt, nil
}

