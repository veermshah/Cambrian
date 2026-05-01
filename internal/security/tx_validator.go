package security

import (
	"errors"
	"fmt"

	"github.com/veermshah/cambrian/internal/chain"
)

// ValidationInput is everything the tx validator needs to make a
// pre-flight decision. Bundled into a struct (rather than a long arg
// list) because every monitor / strategist call site already builds it
// from its own context — keeping the shape stable lets us add fields
// (gas cap, value cap) without rewriting callers.
type ValidationInput struct {
	Tx                    *chain.Transaction
	Sim                   *chain.SimResult
	Destination           string  // chain-specific program / contract address
	EstimatedSlippagePct  float64 // realized slippage from the most recent quote
	MaxAllowedSlippagePct float64 // policy ceiling — usually genome-derived
}

// ValidationError is the structured error every Validate failure returns.
// Reason is a stable code that the monitor's circuit breaker keys on;
// Detail is the human-readable expansion that ends up in logs.
type ValidationError struct {
	Reason string
	Detail string
}

// Stable reason codes — keep these short and grep-friendly. The monitor
// (chunk 23) consumes them; do not change without coordinating.
const (
	ReasonNilTx              = "nil_transaction"
	ReasonNilSim             = "nil_sim_result"
	ReasonSimRevert          = "sim_revert"
	ReasonSlippageExceeded   = "slippage_exceeded"
	ReasonDestinationDenied  = "destination_not_allowed"
	ReasonMissingDestination = "missing_destination"
)

func (e *ValidationError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("tx validation: %s", e.Reason)
	}
	return fmt.Sprintf("tx validation: %s — %s", e.Reason, e.Detail)
}

// Validate runs the three checks spec line 499 calls out, in this order:
//
//  1. Simulation succeeded (SimResult.WouldSucceed == true).
//  2. EstimatedSlippagePct < MaxAllowedSlippagePct.
//  3. Destination address is on the per-chain allowlist.
//
// First failure short-circuits — the caller only needs one reason to
// reject. Returns nil on a clean transaction.
func Validate(in ValidationInput) error {
	if in.Tx == nil {
		return &ValidationError{Reason: ReasonNilTx, Detail: "ValidationInput.Tx is nil"}
	}
	if in.Sim == nil {
		return &ValidationError{Reason: ReasonNilSim, Detail: "ValidationInput.Sim is nil"}
	}
	// Spec line 499: "Transaction simulation before every mainnet submission."
	// Our SimResult uses WouldSucceed (the inverse of the spec's WouldRevert);
	// reject when the sim says the tx would fail.
	if !in.Sim.WouldSucceed {
		return &ValidationError{
			Reason: ReasonSimRevert,
			Detail: in.Sim.ErrorMsg,
		}
	}
	if in.MaxAllowedSlippagePct <= 0 {
		// Treat a zero/negative cap as a missing policy and reject — the
		// strategist's genome supplies a positive value, and 0 would
		// silently allow any slippage.
		return &ValidationError{
			Reason: ReasonSlippageExceeded,
			Detail: "MaxAllowedSlippagePct must be > 0",
		}
	}
	if in.EstimatedSlippagePct >= in.MaxAllowedSlippagePct {
		return &ValidationError{
			Reason: ReasonSlippageExceeded,
			Detail: fmt.Sprintf("estimated %.4f%% >= cap %.4f%%", in.EstimatedSlippagePct, in.MaxAllowedSlippagePct),
		}
	}
	if in.Destination == "" {
		return &ValidationError{
			Reason: ReasonMissingDestination,
			Detail: "Destination required for allowlist check",
		}
	}
	if !IsDestinationAllowed(in.Tx.Chain, in.Destination) {
		return &ValidationError{
			Reason: ReasonDestinationDenied,
			Detail: fmt.Sprintf("destination %s not on %s allowlist", in.Destination, in.Tx.Chain),
		}
	}
	return nil
}

// AsValidationError unwraps err to a *ValidationError, returning nil if
// it isn't one. Convenience for callers that switch on Reason without
// type-assertion boilerplate.
func AsValidationError(err error) *ValidationError {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve
	}
	return nil
}
