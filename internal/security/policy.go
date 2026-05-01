package security

import (
	"strings"
	"sync"
)

// Per-chain destination allowlists. The tx validator enforces that every
// outbound transaction targets a known program / contract — instruction
// hijacking defense, spec line 499 ("strategist outputs config
// adjustments only, never constructs transactions"). Even when the
// strategist is well-behaved, an external dependency (Jupiter, 1inch)
// occasionally rotates router addresses; rotating the allowlist is a
// deliberate, reviewable code change.
//
// Anything not on the allowlist is rejected. Tests can register extra
// addresses via AllowDestination(chain, addr) — production code should
// not call it.

// destinationAllowlists holds the canonical per-chain set of allowed
// program / contract addresses. Lookup is case-insensitive (EVM
// addresses are checksummed; Solana base58 is case-sensitive but
// program IDs in our list are stored verbatim).
var (
	allowMu             sync.RWMutex
	destinationAllowlists = map[string]map[string]struct{}{
		"solana": {
			// Jupiter v6 program — every aggregator swap routes through this.
			"JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4": {},
			// Token program — used by SPL transfers.
			"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA": {},
			// MarginFi v2 — lending positions read/write.
			"MFv2hWf31Z9kbCa1snEPYctwafyhdvnV7FZnsebVacA": {},
			// Kamino lending v2.
			"KLend2g3cP87fffoy8q1mQqGKjrxjC8boSyAYavgmjD": {},
		},
		"base": {
			// 1inch v6 router (Base mainnet — same address as other EVM chains).
			"0x111111125421ca6dc452d289314280a0f8842a65": {},
			// Aave V3 Pool (Base mainnet).
			"0xa238dd80c259a72e81d7e4664a9801593f98d1c5": {},
			// Morpho Blue (Base mainnet).
			"0xbbbbbbbbbb9cc5e90e3b3af64bdaf62c37eeffcb": {},
			// Base WETH predeploy.
			"0x4200000000000000000000000000000000000006": {},
		},
	}
)

// IsDestinationAllowed reports whether the given address is on the
// per-chain allowlist. Both inputs are normalized to lowercase before
// lookup so EVM checksum casing is irrelevant.
func IsDestinationAllowed(chainName, address string) bool {
	allowMu.RLock()
	defer allowMu.RUnlock()
	set, ok := destinationAllowlists[strings.ToLower(chainName)]
	if !ok {
		return false
	}
	if _, ok := set[address]; ok {
		return true
	}
	// Lowercase fallback for EVM addresses where the caller may have
	// passed a checksummed (mixed-case) variant.
	if _, ok := set[strings.ToLower(address)]; ok {
		return true
	}
	return false
}

// AllowDestination adds an address to the named chain's allowlist.
// Tests use it to register fixture addresses; production code should
// edit destinationAllowlists directly so the change shows up in review.
func AllowDestination(chainName, address string) {
	allowMu.Lock()
	defer allowMu.Unlock()
	cn := strings.ToLower(chainName)
	if _, ok := destinationAllowlists[cn]; !ok {
		destinationAllowlists[cn] = map[string]struct{}{}
	}
	destinationAllowlists[cn][address] = struct{}{}
}

// ResetDestinationAllowlistsForTest reverts to the canonical default.
// Tests that call AllowDestination must defer this in cleanup so they
// don't leak state into siblings.
func ResetDestinationAllowlistsForTest() {
	allowMu.Lock()
	defer allowMu.Unlock()
	destinationAllowlists = map[string]map[string]struct{}{
		"solana": {
			"JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4":  {},
			"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA": {},
			"MFv2hWf31Z9kbCa1snEPYctwafyhdvnV7FZnsebVacA":  {},
			"KLend2g3cP87fffoy8q1mQqGKjrxjC8boSyAYavgmjD":  {},
		},
		"base": {
			"0x111111125421ca6dc452d289314280a0f8842a65": {},
			"0xa238dd80c259a72e81d7e4664a9801593f98d1c5": {},
			"0xbbbbbbbbbb9cc5e90e3b3af64bdaf62c37eeffcb": {},
			"0x4200000000000000000000000000000000000006": {},
		},
	}
}
