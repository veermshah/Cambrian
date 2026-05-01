package security

import (
	"errors"
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/chain"
)

// jupiterProgramID is on the default Solana allowlist — used as the
// "good destination" across these tests.
const jupiterProgramID = "JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4"
const oneInchRouter = "0x111111125421ca6dc452d289314280a0f8842a65"

func TestValidateAcceptsCleanTransaction(t *testing.T) {
	in := ValidationInput{
		Tx:                    &chain.Transaction{Chain: "solana", Raw: []byte("rawbytes")},
		Sim:                   &chain.SimResult{WouldSucceed: true},
		Destination:           jupiterProgramID,
		EstimatedSlippagePct:  0.3,
		MaxAllowedSlippagePct: 1.0,
	}
	if err := Validate(in); err != nil {
		t.Fatalf("Validate clean tx: %v", err)
	}
}

func TestValidateTableDriven(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*ValidationInput)
		wantReason string
	}{
		{
			name: "nil tx",
			mutate: func(in *ValidationInput) {
				in.Tx = nil
			},
			wantReason: ReasonNilTx,
		},
		{
			name: "nil sim",
			mutate: func(in *ValidationInput) {
				in.Sim = nil
			},
			wantReason: ReasonNilSim,
		},
		{
			name: "sim revert",
			mutate: func(in *ValidationInput) {
				in.Sim = &chain.SimResult{WouldSucceed: false, ErrorMsg: "out of gas"}
			},
			wantReason: ReasonSimRevert,
		},
		{
			name: "slippage equals cap is rejected",
			mutate: func(in *ValidationInput) {
				in.EstimatedSlippagePct = 1.0
				in.MaxAllowedSlippagePct = 1.0
			},
			wantReason: ReasonSlippageExceeded,
		},
		{
			name: "slippage exceeds cap",
			mutate: func(in *ValidationInput) {
				in.EstimatedSlippagePct = 2.0
				in.MaxAllowedSlippagePct = 1.0
			},
			wantReason: ReasonSlippageExceeded,
		},
		{
			name: "missing slippage cap",
			mutate: func(in *ValidationInput) {
				in.MaxAllowedSlippagePct = 0
			},
			wantReason: ReasonSlippageExceeded,
		},
		{
			name: "missing destination",
			mutate: func(in *ValidationInput) {
				in.Destination = ""
			},
			wantReason: ReasonMissingDestination,
		},
		{
			name: "destination not on allowlist",
			mutate: func(in *ValidationInput) {
				in.Destination = "JsomeRandomProgramIDthatIsNotAllowed11111111"
			},
			wantReason: ReasonDestinationDenied,
		},
		{
			name: "wrong chain for destination",
			mutate: func(in *ValidationInput) {
				in.Tx.Chain = "base"
				in.Destination = jupiterProgramID // solana addr on base allowlist
			},
			wantReason: ReasonDestinationDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := ValidationInput{
				Tx:                    &chain.Transaction{Chain: "solana", Raw: []byte("rawbytes")},
				Sim:                   &chain.SimResult{WouldSucceed: true},
				Destination:           jupiterProgramID,
				EstimatedSlippagePct:  0.3,
				MaxAllowedSlippagePct: 1.0,
			}
			tc.mutate(&in)
			err := Validate(in)
			if err == nil {
				t.Fatalf("Validate: want error with reason %q, got nil", tc.wantReason)
			}
			ve := AsValidationError(err)
			if ve == nil {
				t.Fatalf("err is not a *ValidationError: %v", err)
			}
			if ve.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q (full: %v)", ve.Reason, tc.wantReason, err)
			}
		})
	}
}

func TestValidateBaseClean(t *testing.T) {
	in := ValidationInput{
		Tx:                    &chain.Transaction{Chain: "base", Raw: []byte("rlp")},
		Sim:                   &chain.SimResult{WouldSucceed: true, GasEstimate: 200000},
		Destination:           oneInchRouter,
		EstimatedSlippagePct:  0.5,
		MaxAllowedSlippagePct: 1.0,
	}
	if err := Validate(in); err != nil {
		t.Fatalf("Validate base tx: %v", err)
	}
}

func TestValidateChecksumDestinationMatches(t *testing.T) {
	// EVM checksum casing varies; allowlist matches are case-insensitive.
	in := ValidationInput{
		Tx:                    &chain.Transaction{Chain: "base", Raw: []byte("rlp")},
		Sim:                   &chain.SimResult{WouldSucceed: true},
		Destination:           "0x111111125421CA6DC452D289314280A0F8842A65", // checksummed
		EstimatedSlippagePct:  0.1,
		MaxAllowedSlippagePct: 1.0,
	}
	if err := Validate(in); err != nil {
		t.Fatalf("Validate with checksummed addr: %v", err)
	}
}

func TestValidationErrorString(t *testing.T) {
	ve := &ValidationError{Reason: ReasonSimRevert, Detail: "boom"}
	if !strings.Contains(ve.Error(), "sim_revert") || !strings.Contains(ve.Error(), "boom") {
		t.Errorf("Error() = %q", ve.Error())
	}
	veNoDetail := &ValidationError{Reason: ReasonNilTx}
	if !strings.Contains(veNoDetail.Error(), "nil_transaction") {
		t.Errorf("Error() = %q", veNoDetail.Error())
	}
}

func TestAsValidationErrorOnNonValidationErrorReturnsNil(t *testing.T) {
	if AsValidationError(errors.New("plain")) != nil {
		t.Error("AsValidationError on plain error: want nil")
	}
	if AsValidationError(nil) != nil {
		t.Error("AsValidationError(nil): want nil")
	}
}

func TestAllowDestinationAndReset(t *testing.T) {
	defer ResetDestinationAllowlistsForTest()
	if IsDestinationAllowed("solana", "Custom1111111111111111111111111111111111111") {
		t.Fatal("custom dest should not be allowed before AllowDestination")
	}
	AllowDestination("solana", "Custom1111111111111111111111111111111111111")
	if !IsDestinationAllowed("solana", "Custom1111111111111111111111111111111111111") {
		t.Fatal("AllowDestination did not register")
	}
	ResetDestinationAllowlistsForTest()
	if IsDestinationAllowed("solana", "Custom1111111111111111111111111111111111111") {
		t.Fatal("Reset did not clear custom allow")
	}
}

func TestIsDestinationUnknownChain(t *testing.T) {
	if IsDestinationAllowed("ethereum", oneInchRouter) {
		t.Error("unknown chain should not match")
	}
}
