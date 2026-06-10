package e2e

import (
	"errors"
	"testing"

	"github.com/veermshah/cambrian/internal/chain"
	"github.com/veermshah/cambrian/internal/security"
)

// TestSecurity_TxValidatorCatchesMalformed — spec line 1197.
// Pure-logic. Three classic failure modes: nil tx, sim-revert, slippage
// breach. Every one should produce a structured ValidationError with a
// stable Reason code the monitor can key on.
func TestSecurity_TxValidatorCatchesMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   security.ValidationInput
		want string
	}{
		{
			name: "nil tx",
			in:   security.ValidationInput{},
			want: security.ReasonNilTx,
		},
		{
			name: "nil sim",
			in:   security.ValidationInput{Tx: &chain.Transaction{}},
			want: security.ReasonNilSim,
		},
		{
			name: "sim revert",
			in: security.ValidationInput{
				Tx:  &chain.Transaction{},
				Sim: &chain.SimResult{WouldSucceed: false},
			},
			want: security.ReasonSimRevert,
		},
		{
			name: "slippage exceeded",
			in: security.ValidationInput{
				Tx:                    &chain.Transaction{},
				Sim:                   &chain.SimResult{WouldSucceed: true},
				EstimatedSlippagePct:  0.05,
				MaxAllowedSlippagePct: 0.01,
				Destination:           "test-program",
			},
			want: security.ReasonSlippageExceeded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := security.Validate(tc.in)
			if err == nil {
				t.Fatalf("expected error")
			}
			var ve *security.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("not a ValidationError: %v", err)
			}
			if ve.Reason != tc.want {
				t.Errorf("reason = %q, want %q", ve.Reason, tc.want)
			}
		})
	}
}
