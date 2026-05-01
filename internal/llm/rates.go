package llm

// Per-model token rates as of 2026-04. Anthropic and OpenAI quote per
// million tokens; we store dollars-per-token directly so CalculateCost
// is a single multiplication. Rates are the public list price — discounts
// (Anthropic prompt caching, OpenAI batch tier) land in the rates field
// when the orchestrator opts in.
//
// Update procedure: when the user notes a rate change in the PR
// description, bump the values here and the rates_test.go expectations.
// Never look up rates at runtime — the strategist's fitness signal
// depends on them being deterministic across runs.
//
// Source links (current as of snapshot date):
//   Anthropic: https://www.anthropic.com/pricing
//   OpenAI:    https://openai.com/api/pricing/

// RatesSnapshotDate is the YYYY-MM-DD this table was verified.
const RatesSnapshotDate = "2026-04-15"

// ModelRate is dollars per single token (so the constants below look
// small — multiply per-million by 1e-6 mentally when comparing to docs).
type ModelRate struct {
	InputPerToken  float64
	OutputPerToken float64
}

// modelRates is the canonical rate table. Keys are the model IDs
// callers pass to llm.Get(). Anthropic IDs match those listed in spec
// line 413 and the chunk 6 prompt. OpenAI IDs match the public API.
var modelRates = map[string]ModelRate{
	// Anthropic — input / output per 1M tokens (USD).
	//   haiku-4-5:  $1.00 / $5.00
	//   sonnet-4-6: $3.00 / $15.00
	//   opus-4-7:   $15.00 / $75.00
	"claude-haiku-4-5-20251001": {InputPerToken: 1.00 / 1_000_000, OutputPerToken: 5.00 / 1_000_000},
	"claude-sonnet-4-6":         {InputPerToken: 3.00 / 1_000_000, OutputPerToken: 15.00 / 1_000_000},
	"claude-opus-4-7":           {InputPerToken: 15.00 / 1_000_000, OutputPerToken: 75.00 / 1_000_000},

	// OpenAI — input / output per 1M tokens (USD).
	//   gpt-4o:      $2.50 / $10.00
	//   gpt-4o-mini: $0.15 / $0.60
	"gpt-4o":      {InputPerToken: 2.50 / 1_000_000, OutputPerToken: 10.00 / 1_000_000},
	"gpt-4o-mini": {InputPerToken: 0.15 / 1_000_000, OutputPerToken: 0.60 / 1_000_000},
}

// RateFor returns the rate row for the given model, or zero values when
// the model is unknown. Callers should validate the model name via the
// registry before calling.
func RateFor(model string) (ModelRate, bool) {
	r, ok := modelRates[model]
	return r, ok
}

// computeCost is the shared implementation used by every provider's
// CalculateCost. Lives here (not in each provider file) so that a
// rate-table edit is the only place a price change touches.
func computeCost(model string, inputTokens, outputTokens int) float64 {
	r, ok := modelRates[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)*r.InputPerToken + float64(outputTokens)*r.OutputPerToken
}
