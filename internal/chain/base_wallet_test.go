package chain_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/veermshah/cambrian/internal/chain"
)

func TestBaseWalletRoundTrip(t *testing.T) {
	mk := mustMasterKey(t)

	w, err := chain.NewBaseWallet(mk)
	if err != nil {
		t.Fatalf("NewBaseWallet: %v", err)
	}
	if w.Chain != "base" {
		t.Fatalf("chain = %q, want base", w.Chain)
	}
	if !common.IsHexAddress(w.Address) {
		t.Fatalf("address %q is not a valid hex address", w.Address)
	}

	priv, err := chain.UnsealBaseWallet(mk, w)
	if err != nil {
		t.Fatalf("UnsealBaseWallet: %v", err)
	}
	derived := crypto.PubkeyToAddress(priv.PublicKey).Hex()
	if derived != w.Address {
		t.Fatalf("unseal address mismatch: %q vs %q", derived, w.Address)
	}
}

func TestBaseWalletFromPrivKeyDeterministic(t *testing.T) {
	mk := mustMasterKey(t)
	key := bytes.Repeat([]byte{0x33}, 32)

	w1, err := chain.BaseWalletFromPrivKey(mk, key)
	if err != nil {
		t.Fatalf("BaseWalletFromPrivKey: %v", err)
	}
	w2, err := chain.BaseWalletFromPrivKey(mk, key)
	if err != nil {
		t.Fatalf("BaseWalletFromPrivKey (2): %v", err)
	}
	if w1.Address != w2.Address {
		t.Fatalf("address differs across encrypts of same key: %q vs %q", w1.Address, w2.Address)
	}
	if bytes.Equal(w1.KeyEncrypted, w2.KeyEncrypted) {
		t.Fatal("nonce reuse: ciphertext identical across encrypts")
	}

	priv, err := chain.UnsealBaseWallet(mk, w1)
	if err != nil {
		t.Fatalf("UnsealBaseWallet: %v", err)
	}
	if !bytes.Equal(crypto.FromECDSA(priv), key) {
		t.Fatal("decrypted key does not match input")
	}
}

func TestBaseWalletWrongMasterKeyFails(t *testing.T) {
	mk := mustMasterKey(t)
	other := mustMasterKey(t)

	w, err := chain.NewBaseWallet(mk)
	if err != nil {
		t.Fatalf("NewBaseWallet: %v", err)
	}
	if _, err := chain.UnsealBaseWallet(other, w); err == nil {
		t.Fatal("UnsealBaseWallet with wrong key: want error")
	}
}

func TestBaseWalletRejectsBadInputs(t *testing.T) {
	if _, err := chain.NewBaseWallet(make([]byte, 31)); err == nil {
		t.Fatal("NewBaseWallet 31-byte master key: want error")
	}
	if _, err := chain.BaseWalletFromPrivKey(make([]byte, 31), make([]byte, 32)); err == nil {
		t.Fatal("BaseWalletFromPrivKey 31-byte master key: want error")
	}
	if _, err := chain.BaseWalletFromPrivKey(make([]byte, 32), make([]byte, 16)); err == nil {
		t.Fatal("BaseWalletFromPrivKey 16-byte priv: want error")
	}
}

func TestUnsealBaseRejectsWrongChain(t *testing.T) {
	mk := mustMasterKey(t)
	w := &chain.Wallet{Chain: "solana", Address: "abc", KeyEncrypted: []byte{}}
	_, err := chain.UnsealBaseWallet(mk, w)
	if err == nil || !strings.Contains(err.Error(), "chain mismatch") {
		t.Fatalf("UnsealBaseWallet solana wallet: want chain mismatch, got %v", err)
	}
}
