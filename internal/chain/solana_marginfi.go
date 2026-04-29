package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// MarginFi v2 program. The on-chain MarginfiAccount layout is documented
// at https://github.com/mrgnlabs/mrgn-ts (see lib/marginfi-client-v2);
// the fields we need from each account are:
//
//   off  size  field
//     0    8   discriminator (anchor sighash for "account:MarginfiAccount")
//     8   32   group:     Pubkey
//    40   32   authority: Pubkey
//    72  ...   lendingAccount.balances[16]
//
// Each Balance is 104 bytes:
//
//   off  size  field
//     0    1   active (0/1)
//     1   32   bankPk: Pubkey
//    33    7   padding (bankAssetTag + alignment)
//    40   16   assetShares:           WrappedI80F48 (i128, q48)
//    56   16   liabilityShares:       WrappedI80F48
//    72   16   emissionsOutstanding:  WrappedI80F48
//    88    8   lastUpdate: u64
//    96    8   padding
//
// We don't need oracle prices to find candidate liquidation targets; the
// strategist task in chunk 26 folds in the oracle layer to compute the
// real health factor. Here we surface authority + active bank balances
// in human-readable units.

const (
	marginFiProgramID  = "MFv2hWf31Z9kbCa1snEPYctwafyhdvnV7FZnsebVacA"
	marginFiAccountLen = 8 + 32 + 32 + (16 * 104) + 8 // disc+group+authority+balances+flags
	marginFiBalanceLen = 104
	marginFiBalancesOffset = 8 + 32 + 32              // 72
	marginFiNumBalances    = 16
)

// marginFiAccountDiscriminator is the anchor sighash for
// "account:MarginfiAccount" (sha256("account:MarginfiAccount")[:8]).
// Hardcoded so we don't need a sighash dep; reproduced in the test as
// a cross-check.
var marginFiAccountDiscriminator = [8]byte{0x43, 0xb2, 0x82, 0x6d, 0x7e, 0x72, 0x1c, 0x2a}

// MarginFiBalance is one decoded slot from a MarginAccount.
type MarginFiBalance struct {
	Active            bool
	BankPubkey        solana.PublicKey
	AssetShares       float64 // human-readable (WrappedI80F48 / 2^48)
	LiabilityShares   float64
}

// MarginFiAccount is the subset of fields we parse from MarginAccount.
type MarginFiAccount struct {
	Group     solana.PublicKey
	Authority solana.PublicKey
	Balances  []MarginFiBalance
}

// parseMarginFiAccount decodes raw account data. Rejects accounts whose
// discriminator does not match, so callers can fan over getProgramAccounts
// without pre-filtering by program ownership.
func parseMarginFiAccount(data []byte) (*MarginFiAccount, error) {
	if len(data) < marginFiBalancesOffset+marginFiNumBalances*marginFiBalanceLen {
		return nil, fmt.Errorf("marginfi: account too short: %d bytes", len(data))
	}
	for i := range marginFiAccountDiscriminator {
		if data[i] != marginFiAccountDiscriminator[i] {
			return nil, errors.New("marginfi: discriminator mismatch")
		}
	}
	out := &MarginFiAccount{
		Group:     solana.PublicKeyFromBytes(data[8:40]),
		Authority: solana.PublicKeyFromBytes(data[40:72]),
		Balances:  make([]MarginFiBalance, 0, marginFiNumBalances),
	}
	for i := 0; i < marginFiNumBalances; i++ {
		off := marginFiBalancesOffset + i*marginFiBalanceLen
		bal := MarginFiBalance{
			Active:          data[off] != 0,
			BankPubkey:      solana.PublicKeyFromBytes(data[off+1 : off+33]),
			AssetShares:     decodeWrappedI80F48(data[off+40 : off+56]),
			LiabilityShares: decodeWrappedI80F48(data[off+56 : off+72]),
		}
		if !bal.Active {
			continue
		}
		out.Balances = append(out.Balances, bal)
	}
	return out, nil
}

// fetchMarginFiPositions queries getProgramAccounts for MarginFi accounts,
// filtered by discriminator, and maps each MarginAccount into one or more
// LendingPosition entries (one per active balance).
//
// rpcClient must be non-nil. Returns an empty slice (not nil) if the RPC
// call succeeds but there are no matching accounts.
func fetchMarginFiPositions(ctx context.Context, rpcClient *rpc.Client) ([]LendingPosition, error) {
	if rpcClient == nil {
		return nil, errors.New("marginfi: nil rpc client")
	}
	pid, err := solana.PublicKeyFromBase58(marginFiProgramID)
	if err != nil {
		return nil, fmt.Errorf("marginfi: program id: %w", err)
	}
	disc := solana.Base58(marginFiAccountDiscriminator[:])
	out, err := rpcClient.GetProgramAccountsWithOpts(ctx, pid, &rpc.GetProgramAccountsOpts{
		Encoding: solana.EncodingBase64,
		Filters: []rpc.RPCFilter{
			{Memcmp: &rpc.RPCFilterMemcmp{Offset: 0, Bytes: disc}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marginfi: getProgramAccounts: %w", err)
	}
	positions := make([]LendingPosition, 0, len(out))
	for _, acct := range out {
		if acct.Account == nil || acct.Account.Data == nil {
			continue
		}
		parsed, err := parseMarginFiAccount(acct.Account.Data.GetBinary())
		if err != nil {
			continue // skip junk; never poison the whole batch
		}
		for _, bal := range parsed.Balances {
			positions = append(positions, LendingPosition{
				Chain:           "solana",
				Protocol:        "marginfi",
				Owner:           parsed.Authority.String(),
				CollateralAsset: bal.BankPubkey.String(),
				CollateralAmt:   bal.AssetShares,
				DebtAsset:       bal.BankPubkey.String(),
				DebtAmt:         bal.LiabilityShares,
				HealthFactor:    0, // oracle integration: chunk 26
				LiquidationBonus: 0,
			})
		}
	}
	return positions, nil
}

// decodeWrappedI80F48 converts a 16-byte little-endian i128 in Q48 fixed
// point to a float64. Division by 2^48 is exact for the magnitudes the
// swarm sees in practice; values above ~2^75 lose precision but those
// would be outside any sane lending balance.
func decodeWrappedI80F48(b []byte) float64 {
	if len(b) != 16 {
		return 0
	}
	// Build big.Int from little-endian, two's complement.
	be := make([]byte, 16)
	for i := 0; i < 16; i++ {
		be[i] = b[15-i]
	}
	negative := be[0]&0x80 != 0
	if negative {
		// Flip to get magnitude: ~be + 1
		for i := range be {
			be[i] = ^be[i]
		}
		// add 1
		for i := len(be) - 1; i >= 0; i-- {
			be[i]++
			if be[i] != 0 {
				break
			}
		}
	}
	mag := new(big.Int).SetBytes(be)
	f := new(big.Float).SetInt(mag)
	f.Quo(f, new(big.Float).SetInt(big.NewInt(1<<48)))
	out, _ := f.Float64()
	if negative {
		out = -out
	}
	return out
}

