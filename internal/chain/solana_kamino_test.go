package chain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/big"
	"testing"

	"github.com/gagliardetto/solana-go"
)

func TestKaminoDiscriminator(t *testing.T) {
	sum := sha256.Sum256([]byte("account:Obligation"))
	if !bytes.Equal(sum[:8], kaminoObligationDiscriminator[:]) {
		t.Fatalf("discriminator drift:\n got=%x\nwant=%x", kaminoObligationDiscriminator, sum[:8])
	}
}

// writeQ60 packs `whole` into 16 LE bytes as a Q60 scaled fraction.
func writeQ60(dst []byte, whole int64) {
	bi := new(big.Int).SetInt64(whole)
	bi.Lsh(bi, kaminoScaledFractionShift)
	be := bi.Bytes()
	pad := make([]byte, 16)
	copy(pad[16-len(be):], be)
	for i := 0; i < 16; i++ {
		dst[i] = pad[15-i]
	}
}

func craftKaminoObligation(t *testing.T, market, owner, depositRes, borrowRes solana.PublicKey, depAmount uint64, depUSD, borrowedUSD int64) []byte {
	t.Helper()
	buf := make([]byte, kaminoObligationMinLen)
	copy(buf[:8], kaminoObligationDiscriminator[:])
	copy(buf[32:64], market[:])
	copy(buf[64:96], owner[:])

	// First deposit slot.
	depOff := kaminoObligationHeaderLen
	copy(buf[depOff:depOff+32], depositRes[:])
	binary.LittleEndian.PutUint64(buf[depOff+32:depOff+40], depAmount)
	writeQ60(buf[depOff+40:depOff+56], depUSD)

	// depositedValue total
	writeQ60(buf[1192:1208], depUSD)

	// First borrow slot.
	borrowOff := kaminoBorrowsOffset
	copy(buf[borrowOff:borrowOff+32], borrowRes[:])
	writeQ60(buf[borrowOff+56:borrowOff+72], borrowedUSD)
	writeQ60(buf[borrowOff+72:borrowOff+88], borrowedUSD)
	return buf
}

func TestParseKaminoObligationSuccess(t *testing.T) {
	market := solana.NewWallet().PublicKey()
	owner := solana.NewWallet().PublicKey()
	depRes := solana.NewWallet().PublicKey()
	borrowRes := solana.NewWallet().PublicKey()

	blob := craftKaminoObligation(t, market, owner, depRes, borrowRes, 1_000_000, 1500, 600)
	got, err := parseKaminoObligation(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Owner != owner {
		t.Errorf("owner mismatch")
	}
	if got.LendingMarket != market {
		t.Errorf("market mismatch")
	}
	if len(got.Deposits) != 1 || got.Deposits[0].Reserve != depRes {
		t.Fatalf("deposits = %+v", got.Deposits)
	}
	if got.Deposits[0].DepositedAmount != 1_000_000 {
		t.Errorf("DepositedAmount = %d, want 1_000_000", got.Deposits[0].DepositedAmount)
	}
	if math.Abs(got.Deposits[0].MarketValueUSD-1500) > 1e-9 {
		t.Errorf("MarketValueUSD = %v, want 1500", got.Deposits[0].MarketValueUSD)
	}
	if len(got.Borrows) != 1 || got.Borrows[0].Reserve != borrowRes {
		t.Fatalf("borrows = %+v", got.Borrows)
	}
	if math.Abs(got.Borrows[0].BorrowedAmount-600) > 1e-9 {
		t.Errorf("BorrowedAmount = %v, want 600", got.Borrows[0].BorrowedAmount)
	}
	if math.Abs(got.DepositedValue-1500) > 1e-9 {
		t.Errorf("DepositedValue = %v, want 1500", got.DepositedValue)
	}
}

func TestParseKaminoObligationRejectsBadDiscriminator(t *testing.T) {
	buf := make([]byte, kaminoObligationMinLen)
	for i := 0; i < 8; i++ {
		buf[i] = 0xff
	}
	if _, err := parseKaminoObligation(buf); err == nil {
		t.Fatal("expected discriminator mismatch error")
	}
}

func TestParseKaminoObligationRejectsTruncated(t *testing.T) {
	if _, err := parseKaminoObligation(make([]byte, 100)); err == nil {
		t.Fatal("expected too-short error")
	}
}

func TestDecodeQ60ZeroAndPositive(t *testing.T) {
	zero := make([]byte, 16)
	if got := decodeQ60(zero); got != 0 {
		t.Fatalf("decodeQ60(zero) = %v", got)
	}
	buf := make([]byte, 16)
	writeQ60(buf, 42)
	if got := decodeQ60(buf); math.Abs(got-42) > 1e-9 {
		t.Fatalf("decodeQ60 = %v, want 42", got)
	}
}
