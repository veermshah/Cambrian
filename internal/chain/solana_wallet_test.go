package chain_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/gagliardetto/solana-go"

	"github.com/veermshah/cambrian/internal/chain"
)

func mustMasterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestSolanaWalletRoundTrip(t *testing.T) {
	mk := mustMasterKey(t)

	w, err := chain.NewSolanaWallet(mk)
	if err != nil {
		t.Fatalf("NewSolanaWallet: %v", err)
	}
	if w.Chain != "solana" {
		t.Fatalf("chain = %q, want solana", w.Chain)
	}
	if _, err := solana.PublicKeyFromBase58(w.Address); err != nil {
		t.Fatalf("address %q not a valid base58 pubkey: %v", w.Address, err)
	}

	priv, err := chain.UnsealSolanaWallet(mk, w)
	if err != nil {
		t.Fatalf("UnsealSolanaWallet: %v", err)
	}
	derived := priv.PublicKey().String()
	if derived != w.Address {
		t.Fatalf("unseal pubkey mismatch: %q vs %q", derived, w.Address)
	}
}

func TestSolanaWalletFromSeedDeterministic(t *testing.T) {
	mk := mustMasterKey(t)

	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	w1, err := chain.SolanaWalletFromSeed(mk, seed)
	if err != nil {
		t.Fatalf("SolanaWalletFromSeed: %v", err)
	}
	w2, err := chain.SolanaWalletFromSeed(mk, seed)
	if err != nil {
		t.Fatalf("SolanaWalletFromSeed (2): %v", err)
	}
	if w1.Address != w2.Address {
		t.Fatalf("address differs across encrypts of same seed: %q vs %q", w1.Address, w2.Address)
	}
	if bytes.Equal(w1.KeyEncrypted, w2.KeyEncrypted) {
		t.Fatal("nonce reuse: ciphertext identical across encrypts")
	}

	priv, err := chain.UnsealSolanaWallet(mk, w1)
	if err != nil {
		t.Fatalf("UnsealSolanaWallet: %v", err)
	}
	if !bytes.Equal(priv[:ed25519.SeedSize], seed) {
		t.Fatal("decrypted seed does not match input")
	}
}

func TestSolanaWalletWrongMasterKeyFails(t *testing.T) {
	mk := mustMasterKey(t)
	other := mustMasterKey(t)

	w, err := chain.NewSolanaWallet(mk)
	if err != nil {
		t.Fatalf("NewSolanaWallet: %v", err)
	}
	if _, err := chain.UnsealSolanaWallet(other, w); err == nil {
		t.Fatal("UnsealSolanaWallet with wrong key: want error")
	}
}

func TestSolanaWalletRejectsBadMasterKey(t *testing.T) {
	if _, err := chain.NewSolanaWallet(make([]byte, 31)); err == nil {
		t.Fatal("NewSolanaWallet 31-byte master key: want error")
	}
	if _, err := chain.SolanaWalletFromSeed(make([]byte, 31), make([]byte, ed25519.SeedSize)); err == nil {
		t.Fatal("SolanaWalletFromSeed 31-byte master key: want error")
	}
	if _, err := chain.SolanaWalletFromSeed(make([]byte, 32), make([]byte, 16)); err == nil {
		t.Fatal("SolanaWalletFromSeed 16-byte seed: want error")
	}
}

func TestUnsealRejectsWrongChain(t *testing.T) {
	mk := mustMasterKey(t)
	w := &chain.Wallet{Chain: "base", Address: "0xdeadbeef", KeyEncrypted: []byte{}}
	_, err := chain.UnsealSolanaWallet(mk, w)
	if err == nil || !strings.Contains(err.Error(), "chain mismatch") {
		t.Fatalf("UnsealSolanaWallet base wallet: want chain mismatch, got %v", err)
	}
}
