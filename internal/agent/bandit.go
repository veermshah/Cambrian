package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
)

// TickBandit is a Thompson Sampling bandit over Beta(α, β) arms — one
// arm per micro-policy the strategist enabled in the genome's
// BanditPolicies list. Tracks success counts (Alphas) and failure
// counts (Betas), Select() samples each arm and returns the winner,
// Update() reinforces the chosen arm based on the realized reward.
//
// Spec lines 144–163 describe the shape verbatim. Costs zero LLM calls;
// the strategist consumes the resulting per-policy win rates on its
// slow loop to inform config adjustments.
type TickBandit struct {
	mu     sync.Mutex
	Alphas map[string]float64
	Betas  map[string]float64
	rng    *rand.Rand
}

// NewTickBandit seeds a bandit with the given policy list. Every arm
// starts at Beta(1, 1) — uniform prior, so the first few Selects pick
// roughly at random until Update writes evidence.
func NewTickBandit(policies []string) *TickBandit {
	return newBanditWithRNG(policies, defaultRNG())
}

// newBanditWithRNG is the test-friendly constructor: caller supplies
// a deterministic RNG so convergence and round-trip tests are stable.
func newBanditWithRNG(policies []string, rng *rand.Rand) *TickBandit {
	a := make(map[string]float64, len(policies))
	b := make(map[string]float64, len(policies))
	for _, p := range policies {
		a[p] = 1
		b[p] = 1
	}
	return &TickBandit{Alphas: a, Betas: b, rng: rng}
}

// defaultRNG returns an RNG seeded from runtime entropy. math/rand/v2's
// default Source is concurrency-safe and seeded automatically.
func defaultRNG() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

// Policies returns the registered arm names in deterministic order.
// Stable iteration matters because Select breaks ties by first-seen
// order and tests rely on reproducible behavior.
func (b *TickBandit) Policies() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.Alphas))
	for p := range b.Alphas {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Select samples Beta(α, β) for every arm and returns the policy with
// the highest sample. Returns "" if no arms are registered — callers
// should check for that explicitly rather than fall through to a swap.
func (b *TickBandit) Select() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Alphas) == 0 {
		return ""
	}
	bestPolicy := ""
	bestSample := -math.MaxFloat64
	for _, policy := range b.sortedPoliciesLocked() {
		sample := betaSample(b.rng, b.Alphas[policy], b.Betas[policy])
		if sample > bestSample {
			bestSample = sample
			bestPolicy = policy
		}
	}
	return bestPolicy
}

// Update reinforces policy: positive reward bumps α, non-positive bumps
// β. Treats every call as one Bernoulli trial — the strategist can call
// Update multiple times per tick if the policy produced multiple trades.
// Returns an error if the policy isn't registered (Select would already
// have refused to pick it).
func (b *TickBandit) Update(policy string, reward float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.Alphas[policy]; !ok {
		return fmt.Errorf("bandit: unknown policy %q", policy)
	}
	if reward > 0 {
		b.Alphas[policy]++
	} else {
		b.Betas[policy]++
	}
	return nil
}

// AddPolicy registers a new arm at Beta(1, 1). Idempotent — calling
// twice with the same name is a no-op so init paths can register the
// same arm during construction and recovery.
func (b *TickBandit) AddPolicy(policy string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.Alphas[policy]; exists {
		return
	}
	b.Alphas[policy] = 1
	b.Betas[policy] = 1
}

// banditState is the JSON shape persisted by SaveState. Exported fields
// because encoding/json needs them.
type banditState struct {
	Alphas map[string]float64 `json:"alphas"`
	Betas  map[string]float64 `json:"betas"`
}

// SaveState returns a JSON snapshot of the bandit's posteriors. Stored
// in the agents.bandit_state JSONB column so a restart resumes with
// learned weights intact. RNG state is intentionally not persisted —
// sampling is randomized across restarts on purpose.
func (b *TickBandit) SaveState() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return json.Marshal(banditState{Alphas: b.Alphas, Betas: b.Betas})
}

// LoadState replaces the bandit's posteriors from a SaveState blob.
// Refuses input whose alpha/beta key sets don't match — that would
// indicate corruption or a config drift the orchestrator should surface.
func (b *TickBandit) LoadState(blob []byte) error {
	if len(blob) == 0 {
		return errors.New("bandit: empty state")
	}
	var s banditState
	if err := json.Unmarshal(blob, &s); err != nil {
		return fmt.Errorf("bandit: decode state: %w", err)
	}
	if len(s.Alphas) != len(s.Betas) {
		return fmt.Errorf("bandit: alpha/beta size mismatch (%d vs %d)", len(s.Alphas), len(s.Betas))
	}
	for k := range s.Alphas {
		if _, ok := s.Betas[k]; !ok {
			return fmt.Errorf("bandit: missing beta for %q", k)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Alphas = s.Alphas
	b.Betas = s.Betas
	return nil
}

func (b *TickBandit) sortedPoliciesLocked() []string {
	out := make([]string, 0, len(b.Alphas))
	for p := range b.Alphas {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// betaSample draws X ~ Beta(alpha, beta) via the gamma-ratio identity
// X = Ga / (Ga + Gb), where Ga ~ Gamma(alpha, 1) and Gb ~ Gamma(beta, 1).
// Valid for alpha, beta > 0. Our bandit only ever uses values >= 1, so
// the Marsaglia–Tsang gamma sampler is sufficient.
func betaSample(rng *rand.Rand, alpha, beta float64) float64 {
	if alpha <= 0 || beta <= 0 {
		return 0.5
	}
	ga := gammaSample(rng, alpha)
	gb := gammaSample(rng, beta)
	if ga+gb == 0 {
		return 0.5
	}
	return ga / (ga + gb)
}

// gammaSample is Marsaglia & Tsang's "A Simple Method for Generating
// Gamma Variables" (ACM TOMS 2000). Expects shape >= 1; for shape < 1
// we use the multiplicative trick (Stuart 1962) but the bandit never
// goes there in practice.
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		// Boost shape and rescale: X ~ Gamma(shape, 1) is U^(1/shape) * Y
		// where Y ~ Gamma(shape+1, 1) and U ~ Uniform(0, 1).
		y := gammaSample(rng, shape+1)
		u := rng.Float64()
		return y * math.Pow(u, 1.0/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		x := rng.NormFloat64()
		v := 1.0 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1.0-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}
