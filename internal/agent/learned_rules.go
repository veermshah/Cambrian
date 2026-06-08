package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand"
	"sort"
	"strings"
)

// Learned-rule constants — spec lines 186-188.
const (
	// MaxLearnedRules is the per-agent cap from spec line 187 ("up to 10
	// persistent procedural rules"). When Add is called on a full slice
	// the lowest-confidence rule is evicted.
	MaxLearnedRules = 10
	// EMAAlpha is the new-evidence weight in the confidence update
	//   confidence = (1 - α) * confidence + α * (1 if success else 0)
	// α = 0.1 means each new outcome moves the score 10% of the way
	// toward the observed bit. Half-life ≈ log(0.5)/log(0.9) ≈ 6.58
	// observations.
	EMAAlpha = 0.1
	// InheritanceRegression is the multiplier applied to each parent
	// rule's confidence when an offspring inherits it (spec line 1190:
	// "child gets parent's rules, each at 0.7 × parent confidence —
	// regression to mean"). Offspring start humble about inherited
	// wisdom and earn it back through outcomes.
	InheritanceRegression = 0.7
	// DefaultMutationProbability is the base chance a Mutate call
	// rewrites any single rule. Kept low so most generations preserve
	// procedural knowledge.
	DefaultMutationProbability = 0.05
)

// ErrEmptyRuleText is returned when Add is called with a blank Text.
var ErrEmptyRuleText = errors.New("learned_rules: rule text required")

// Add inserts `rule` into `rules`. If the slice is at MaxLearnedRules,
// the lowest-confidence rule is evicted first (ties broken by older
// ID coming out — list order is the tiebreaker). Returns the new
// slice; never mutates the input.
//
//   - Blank rule.Text → ErrEmptyRuleText (rules must carry semantic
//     payload; an empty rule would just waste a slot).
//   - rule.ID empty → a 16-char hex ID is generated.
//   - rule.Confidence clamped to [0, 1].
func Add(rules []LearnedRule, rule LearnedRule) ([]LearnedRule, error) {
	if strings.TrimSpace(rule.Text) == "" {
		return nil, ErrEmptyRuleText
	}
	if rule.ID == "" {
		rule.ID = newRuleID()
	}
	rule.Confidence = clamp01(rule.Confidence)

	out := append([]LearnedRule(nil), rules...)
	if len(out) >= MaxLearnedRules {
		out = evictLowest(out)
	}
	out = append(out, rule)
	return out, nil
}

// RecordOutcome updates the confidence of the rule with the given ID
// using the EMA defined by EMAAlpha. Returns the new slice with the
// update applied; if no rule with the ID exists, returns the input
// unchanged. Pure — input is never mutated.
//
// Derivation:
//
//	c_{n+1} = (1 - α) · c_n + α · o_n
//
// where o_n ∈ {0, 1} is the latest outcome. For α = 0.1 this is the
// standard 10-period EMA over a Bernoulli stream.
func RecordOutcome(rules []LearnedRule, ruleID string, success bool) []LearnedRule {
	out := append([]LearnedRule(nil), rules...)
	observed := 0.0
	if success {
		observed = 1.0
	}
	for i := range out {
		if out[i].ID == ruleID {
			out[i].Confidence = clamp01((1-EMAAlpha)*out[i].Confidence + EMAAlpha*observed)
			return out
		}
	}
	return out
}

// Inherit produces the rule slice an offspring should start with.
// Each parent rule is copied with a fresh ID and Confidence multiplied
// by InheritanceRegression (0.7), implementing the spec's
// regression-to-mean. The cap is enforced — if the parent had more
// than MaxLearnedRules (shouldn't happen, but defensive), the
// lowest-confidence overflow is dropped.
func Inherit(parent []LearnedRule) []LearnedRule {
	out := make([]LearnedRule, 0, len(parent))
	for _, r := range parent {
		out = append(out, LearnedRule{
			ID:         newRuleID(),
			Text:       r.Text,
			Confidence: clamp01(r.Confidence * InheritanceRegression),
		})
	}
	// Defensive cap enforcement: keep the strongest MaxLearnedRules.
	if len(out) > MaxLearnedRules {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
		out = out[:MaxLearnedRules]
	}
	return out
}

// RuleRewriter is the narrow interface Mutate uses to delegate the LLM
// rewrite. The orchestrator wires this to a closure around
// orchestrator.RewriteStrategistPrompt (or a similar helper) so the
// agent package doesn't import orchestrator. Returns the rewritten
// rule text verbatim.
type RuleRewriter interface {
	Rewrite(ctx context.Context, text string) (string, error)
}

// RuleRewriterFunc is the function-literal adapter for RuleRewriter,
// matching the http.HandlerFunc pattern.
type RuleRewriterFunc func(ctx context.Context, text string) (string, error)

// Rewrite implements RuleRewriter.
func (f RuleRewriterFunc) Rewrite(ctx context.Context, text string) (string, error) {
	return f(ctx, text)
}

// MutateConfig parameterises Mutate. Both fields are optional; defaults
// keep mutation low-probability.
type MutateConfig struct {
	// Probability is the per-rule chance of rewriting. Clamped to
	// [0, 1]; zero or negative falls back to DefaultMutationProbability.
	Probability float64
	// Rewriter performs the LLM call. If nil, Mutate is a no-op (the
	// agent still has its rules — they just don't mutate this round).
	Rewriter RuleRewriter
}

// Mutate walks the rules and, with `cfg.Probability` chance per rule,
// rewrites the Text via cfg.Rewriter. A failed rewrite leaves the rule
// untouched and is returned in the error list so the caller can log
// it without losing the genome. The rule's ID and Confidence are
// preserved across mutation — only the text changes.
//
// Returns (new rules slice, per-rule rewrite errors). The error slice
// is nil on full success and otherwise has one entry per failed rule
// (in the same order failures occurred).
func Mutate(ctx context.Context, rng *mrand.Rand, rules []LearnedRule, cfg MutateConfig) ([]LearnedRule, []error) {
	if rng == nil {
		// Fall back to the package-level rand source. Using time-seeded
		// rand here is fine because mutation outcomes are not security-
		// sensitive; the orchestrator should still inject its own RNG
		// for reproducibility.
		rng = mrand.New(mrand.NewSource(1))
	}
	p := cfg.Probability
	if p <= 0 {
		p = DefaultMutationProbability
	}
	if p > 1 {
		p = 1
	}

	out := append([]LearnedRule(nil), rules...)
	if cfg.Rewriter == nil {
		return out, nil
	}
	var errs []error
	for i := range out {
		if rng.Float64() >= p {
			continue
		}
		rewritten, err := cfg.Rewriter.Rewrite(ctx, out[i].Text)
		if err != nil {
			errs = append(errs, fmt.Errorf("mutate rule %s: %w", out[i].ID, err))
			continue
		}
		rewritten = strings.TrimSpace(rewritten)
		if rewritten == "" {
			// Defensive: an empty rewrite would erase the rule. Skip.
			continue
		}
		out[i].Text = rewritten
	}
	return out, errs
}

// evictLowest returns rules with the lowest-confidence entry removed.
// Ties broken by index (earlier index evicted first → FIFO among ties)
// so behaviour is deterministic.
func evictLowest(rules []LearnedRule) []LearnedRule {
	if len(rules) == 0 {
		return rules
	}
	victim := 0
	for i := 1; i < len(rules); i++ {
		if rules[i].Confidence < rules[victim].Confidence {
			victim = i
		}
	}
	return append(rules[:victim:victim], rules[victim+1:]...)
}

// newRuleID returns a short, opaque, URL-safe identifier suitable for
// use as a JSON map key. Collisions are vanishingly improbable for the
// scale we care about (≤ 10 rules per agent × ≤ 10k agents).
func newRuleID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Should never happen on real OSes; fall back to a sentinel so
		// the caller doesn't crash.
		return "rule-fallback"
	}
	return "rule-" + hex.EncodeToString(b[:])
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
