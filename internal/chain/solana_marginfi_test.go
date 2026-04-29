package chain

import (
	"bytes"
	"crypto/sha256"
	"math"
	"math/big"
	"testing"

	"github.com/gagliardetto/solana-go"
)

func TestMarginFiDiscriminatorMatchesAnchorSighash(t *testing.T) {
	sum := sha256.Sum256([]byte("account:MarginfiAccount"))
	if !bytes.Equal(sum[:8], marginFiAccountDiscriminator[:]) {
		t.Fatalf("discriminator drift:\n got=%x\nwant=%x", marginFiAccountDiscriminator, sum[:8])
	}
}

// craftMarginFiAccount builds a synthetic MarginAccount blob with one
// active balance and the rest zeroed. Lets the parser exercise its
// offsets without depending on a live cluster.
func craftMarginFiAccount(t *testing.T, group, authority, bank solana.PublicKey, assetShares, liabShares int64) []byte {
	t.Helper()
	buf := make([]byte, marginFiAccountLen)
	copy(buf[:8], marginFiAccountDiscriminator[:])
	copy(buf[8:40], group[:])
	copy(buf[40:72], authority[:])

	// One active balance at slot 0.
	off := marginFiBalancesOffset
	buf[off] = 1
	copy(buf[off+1:off+33], bank[:])
	writeI80F48(buf[off+40:off+56], assetShares)
	writeI80F48(buf[off+56:off+72], liabShares)
	// remaining 15 slots stay zeroed -> active=false, parser drops them.
	return buf
}

func writeI80F48(dst []byte, whole int64) {
	// Encode `whole << 48` as a 16-byte little-endian two's-complement i128.
	bi := new(big.Int).SetInt64(whole)
	bi.Lsh(bi, 48)
	if bi.Sign() < 0 {
		mod := new(big.Int).Lsh(big.NewInt(1), 128)
		bi.Add(bi, mod)
	}
	be := bi.Bytes()
	pad := make([]byte, 16)
	copy(pad[16-len(be):], be)
	for i := 0; i < 16; i++ {
		dst[i] = pad[15-i]
	}
}

func TestParseMarginFiAccountSuccess(t *testing.T) {
	group := solana.NewWallet().PublicKey()
	auth := solana.NewWallet().PublicKey()
	bank := solana.NewWallet().PublicKey()

	blob := craftMarginFiAccount(t, group, auth, bank, 100, 25)
	got, err := parseMarginFiAccount(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Group != group {
		t.Errorf("group mismatch")
	}
	if got.Authority != auth {
		t.Errorf("authority mismatch")
	}
	if len(got.Balances) != 1 {
		t.Fatalf("balances = %d, want 1", len(got.Balances))
	}
	bal := got.Balances[0]
	if bal.BankPubkey != bank {
		t.Errorf("bank pubkey mismatch")
	}
	if math.Abs(bal.AssetShares-100.0) > 1e-9 {
		t.Errorf("AssetShares = %v, want ~100", bal.AssetShares)
	}
	if math.Abs(bal.LiabilityShares-25.0) > 1e-9 {
		t.Errorf("LiabilityShares = %v, want ~25", bal.LiabilityShares)
	}
}

func TestParseMarginFiAccountRejectsBadDiscriminator(t *testing.T) {
	buf := make([]byte, marginFiAccountLen)
	for i := 0; i < 8; i++ {
		buf[i] = 0xff
	}
	if _, err := parseMarginFiAccount(buf); err == nil {
		t.Fatal("expected discriminator mismatch error")
	}
}

func TestParseMarginFiAccountRejectsTruncated(t *testing.T) {
	if _, err := parseMarginFiAccount(make([]byte, 100)); err == nil {
		t.Fatal("expected too-short error")
	}
}

func TestDecodeWrappedI80F48Negative(t *testing.T) {
	buf := make([]byte, 16)
	writeI80F48(buf, -7)
	got := decodeWrappedI80F48(buf)
	if math.Abs(got+7.0) > 1e-9 {
		t.Fatalf("decode = %v, want -7", got)
	}
}
