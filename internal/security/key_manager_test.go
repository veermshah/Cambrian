package security

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func mustMasterKey(t *testing.T) []byte {
	t.Helper()
	mk := make([]byte, MasterKeySize)
	if _, err := rand.Read(mk); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return mk
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	mk := mustMasterKey(t)
	plaintext := []byte("secp256k1-private-key-32-bytes!!")

	ct, err := Encrypt(plaintext, mk)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ct) <= len(plaintext) {
		t.Errorf("ciphertext should be longer than plaintext; got %d vs %d", len(ct), len(plaintext))
	}

	pt, err := Decrypt(ct, mk)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("roundtrip mismatch: %x != %x", pt, plaintext)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	mk := mustMasterKey(t)
	pt := []byte("same plaintext")

	ct1, err := Encrypt(pt, mk)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	ct2, err := Encrypt(pt, mk)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encrypts of the same plaintext produced identical ciphertext — nonce reuse")
	}
}

func TestDecryptTamperedCiphertextFails(t *testing.T) {
	mk := mustMasterKey(t)
	ct, err := Encrypt([]byte("secret"), mk)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Flip a bit in the body of the ciphertext (past the nonce).
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xff

	if _, err := Decrypt(tampered, mk); err == nil {
		t.Fatal("Decrypt of tampered ciphertext: want error, got nil")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	mk1 := mustMasterKey(t)
	mk2 := mustMasterKey(t)
	ct, err := Encrypt([]byte("secret"), mk1)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(ct, mk2); err == nil {
		t.Fatal("Decrypt with wrong key: want error")
	}
}

func TestEncryptRejectsBadKeyLength(t *testing.T) {
	if _, err := Encrypt([]byte("hi"), make([]byte, 16)); err == nil {
		t.Fatal("Encrypt with 16-byte key: want error")
	}
	if _, err := Decrypt(make([]byte, 64), make([]byte, 31)); err == nil {
		t.Fatal("Decrypt with 31-byte key: want error")
	}
}

func TestDecryptShortCiphertextFails(t *testing.T) {
	mk := mustMasterKey(t)
	if _, err := Decrypt([]byte{0x00}, mk); err == nil {
		t.Fatal("Decrypt of 1-byte input: want error")
	}
}

func TestLoadMasterKeyFromEnv(t *testing.T) {
	t.Setenv("TEST_MK_VAR", strings.Repeat("k", MasterKeySize))
	got, err := LoadMasterKey("TEST_MK_VAR")
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	if len(got) != MasterKeySize {
		t.Errorf("len(got) = %d, want %d", len(got), MasterKeySize)
	}
}

func TestLoadMasterKeyMissing(t *testing.T) {
	t.Setenv("TEST_MK_VAR_MISSING", "")
	if _, err := LoadMasterKey("TEST_MK_VAR_MISSING"); err == nil {
		t.Fatal("LoadMasterKey on empty env: want error")
	}
}

func TestLoadMasterKeyWrongLength(t *testing.T) {
	t.Setenv("TEST_MK_VAR_BAD", "tooshort")
	if _, err := LoadMasterKey("TEST_MK_VAR_BAD"); err == nil {
		t.Fatal("LoadMasterKey on 8-byte env: want error")
	}
}
