package chain

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Kamino lending program (mainnet). Devnet has a separate deployment but
// is rarely populated; the strategist task in chunk 26 owns picking the
// right address for each network.
//
// Obligation account layout (kamino-lending v1.x — see kvr-1.x IDL):
//
//   off    size   field
//      0      8   discriminator (sha256("account:Obligation")[:8])
//      8      8   tag
//     16     16   lastUpdate
//     32     32   lendingMarket: Pubkey
//     64     32   owner: Pubkey
//     96  1088   deposits: [ObligationCollateral; 8]   (each 136 bytes)
//   1184      8   lowestReserveDepositLiquidationLtv
//   1192     16   depositedValueSf: u128 (Q60 scaled fraction)
//   1208    800   borrows: [ObligationBorrow; 5]       (each 160 bytes)
//   2008    ...   trailing fields not parsed here
//
// ObligationCollateral (136 bytes):
//
//   off    size   field
//      0     32   depositReserve: Pubkey
//     32      8   depositedAmount: u64
//     40     16   marketValueSf: u128 (Q60)
//     48      8   borrowedAmountAgainstThisCollateralInElevationGroup
//     56     72   padding ([u64; 9])
//
// ObligationBorrow (160 bytes):
//
//   off    size   field
//      0     32   borrowReserve: Pubkey
//     32     16   cumulativeBorrowRateBf
//     48      8   padding0
//     56     16   borrowedAmountSf: u128 (Q60)
//     72     16   marketValueSf: u128 (Q60)
//     88     16   borrowFactorAdjustedMarketValueSf: u128 (Q60)
//    104     56   padding1 ([u64; 7])

const (
	kaminoProgramID            = "KLend2g3cP87fffoy8q1mQqGKjrxjC8boSyAYavgmjD"
	kaminoObligationHeaderLen  = 96
	kaminoNumDeposits          = 8
	kaminoCollateralSize       = 136
	kaminoNumBorrows           = 5
	kaminoBorrowSize           = 160
	kaminoBorrowsOffset        = kaminoObligationHeaderLen + kaminoNumDeposits*kaminoCollateralSize + 8 + 16 // 1208
	kaminoObligationMinLen     = kaminoBorrowsOffset + kaminoNumBorrows*kaminoBorrowSize                    // 2008
	kaminoScaledFractionShift  = 60
)

// kaminoObligationDiscriminator is the anchor sighash for
// "account:Obligation" — verified by TestKaminoDiscriminator.
var kaminoObligationDiscriminator = [8]byte{0xa8, 0xce, 0x8d, 0x6a, 0x58, 0x4c, 0xac, 0xa7}

// KaminoCollateral is the parsed view of one deposit slot.
type KaminoCollateral struct {
	Reserve         solana.PublicKey
	DepositedAmount uint64
	MarketValueUSD  float64 // depositedValueSf decoded from Q60
}

// KaminoBorrow is the parsed view of one borrow slot.
type KaminoBorrow struct {
	Reserve         solana.PublicKey
	BorrowedAmount  float64 // Q60-decoded
	MarketValueUSD  float64
}

// KaminoObligation is the subset of fields we surface upward.
type KaminoObligation struct {
	LendingMarket  solana.PublicKey
	Owner          solana.PublicKey
	Deposits       []KaminoCollateral
	Borrows        []KaminoBorrow
	DepositedValue float64 // total, Q60-decoded
}

func parseKaminoObligation(data []byte) (*KaminoObligation, error) {
	if len(data) < kaminoObligationMinLen {
		return nil, fmt.Errorf("kamino: account too short: %d bytes", len(data))
	}
	for i := range kaminoObligationDiscriminator {
		if data[i] != kaminoObligationDiscriminator[i] {
			return nil, errors.New("kamino: discriminator mismatch")
		}
	}
	out := &KaminoObligation{
		LendingMarket: solana.PublicKeyFromBytes(data[32:64]),
		Owner:         solana.PublicKeyFromBytes(data[64:96]),
	}
	for i := 0; i < kaminoNumDeposits; i++ {
		off := kaminoObligationHeaderLen + i*kaminoCollateralSize
		amt := binary.LittleEndian.Uint64(data[off+32 : off+40])
		if amt == 0 {
			continue
		}
		out.Deposits = append(out.Deposits, KaminoCollateral{
			Reserve:         solana.PublicKeyFromBytes(data[off : off+32]),
			DepositedAmount: amt,
			MarketValueUSD:  decodeQ60(data[off+40 : off+56]),
		})
	}
	out.DepositedValue = decodeQ60(data[1192:1208])
	for i := 0; i < kaminoNumBorrows; i++ {
		off := kaminoBorrowsOffset + i*kaminoBorrowSize
		borrowed := decodeQ60(data[off+56 : off+72])
		if borrowed == 0 {
			continue
		}
		out.Borrows = append(out.Borrows, KaminoBorrow{
			Reserve:        solana.PublicKeyFromBytes(data[off : off+32]),
			BorrowedAmount: borrowed,
			MarketValueUSD: decodeQ60(data[off+72 : off+88]),
		})
	}
	return out, nil
}

// fetchKaminoPositions queries getProgramAccounts and turns each
// Obligation into one LendingPosition per active borrow (lined up with
// its largest collateral). HealthFactor is computed when both deposit
// and borrow USD values are non-zero.
func fetchKaminoPositions(ctx context.Context, rpcClient *rpc.Client) ([]LendingPosition, error) {
	if rpcClient == nil {
		return nil, errors.New("kamino: nil rpc client")
	}
	pid, err := solana.PublicKeyFromBase58(kaminoProgramID)
	if err != nil {
		return nil, fmt.Errorf("kamino: program id: %w", err)
	}
	disc := solana.Base58(kaminoObligationDiscriminator[:])
	out, err := rpcClient.GetProgramAccountsWithOpts(ctx, pid, &rpc.GetProgramAccountsOpts{
		Encoding: solana.EncodingBase64,
		Filters: []rpc.RPCFilter{
			{Memcmp: &rpc.RPCFilterMemcmp{Offset: 0, Bytes: disc}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("kamino: getProgramAccounts: %w", err)
	}
	positions := make([]LendingPosition, 0, len(out))
	for _, acct := range out {
		if acct.Account == nil || acct.Account.Data == nil {
			continue
		}
		obl, err := parseKaminoObligation(acct.Account.Data.GetBinary())
		if err != nil || len(obl.Borrows) == 0 || len(obl.Deposits) == 0 {
			continue
		}
		// Pick the largest collateral as the representative for this row.
		// Strategist refines per-asset later.
		biggest := obl.Deposits[0]
		for _, d := range obl.Deposits[1:] {
			if d.MarketValueUSD > biggest.MarketValueUSD {
				biggest = d
			}
		}
		for _, b := range obl.Borrows {
			hf := 0.0
			if b.MarketValueUSD > 0 && obl.DepositedValue > 0 {
				hf = obl.DepositedValue / b.MarketValueUSD
			}
			positions = append(positions, LendingPosition{
				Chain:           "solana",
				Protocol:        "kamino",
				Owner:           obl.Owner.String(),
				CollateralAsset: biggest.Reserve.String(),
				CollateralAmt:   biggest.MarketValueUSD,
				DebtAsset:       b.Reserve.String(),
				DebtAmt:         b.MarketValueUSD,
				HealthFactor:    hf,
				LiquidationBonus: 0,
			})
		}
	}
	return positions, nil
}

// decodeQ60 reads a 16-byte little-endian u128 in Q60 fixed point and
// returns float64. Same caveat as decodeWrappedI80F48 — values above
// ~2^75 lose precision; well outside any sane USD market value.
func decodeQ60(b []byte) float64 {
	if len(b) != 16 {
		return 0
	}
	be := make([]byte, 16)
	for i := 0; i < 16; i++ {
		be[i] = b[15-i]
	}
	mag := new(big.Int).SetBytes(be)
	f := new(big.Float).SetInt(mag)
	f.Quo(f, new(big.Float).SetInt(new(big.Int).Lsh(big.NewInt(1), kaminoScaledFractionShift)))
	out, _ := f.Float64()
	return out
}
