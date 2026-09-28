package orchestrator

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/veermshah/cambrian/internal/agent"
)

// Evolution methods persisted into lineage.evolution_method.
const (
	EvolutionMutation  = "mutation"
	EvolutionCrossover = "crossover"
)

// ReproductionConfig tunes the per-epoch offspring proposer.
//
// Parent selection follows the open-ended archive sampling used by the
// Darwin Gödel Machine (Zhang et al., 2025) and AlphaEvolve (Novikov et
// al., 2025): the swarm is treated as a quality-diversity archive keyed
// by (task_type, chain, model) niche, and parents are sampled with
// probability proportional to
//
//	sigmoid((fitness - median) / FitnessTemperatureUSD)
//	  × 1 / (1 + children)                      — "stepping stone" bonus
//	  × (1 + NoveltyWeight × DiversityBonus)    — rare-niche bonus
//
// rather than greedily from the single best agent. Elites in every
// niche stay reachable, so a currently-unprofitable niche can still
// seed a lineage that later wins.
type ReproductionConfig struct {
	// MaxProposalsPerEpoch caps how many new proposals the orchestrator
	// queues each epoch. Default 3.
	MaxProposalsPerEpoch int
	// CrossoverProbability is the chance a proposal is produced by
	// module-level crossover with an elite from another niche instead
	// of mutation. Default 0.25.
	CrossoverProbability float64
	// FitnessTemperatureUSD is the sigmoid scale for net-profit fitness.
	// Default 25 USD.
	FitnessTemperatureUSD float64
	// NoveltyWeight scales the rare-niche bonus. Default 0.5.
	NoveltyWeight float64
}

// DefaultReproductionConfig returns the tuned defaults.
func DefaultReproductionConfig() ReproductionConfig {
	return ReproductionConfig{
		MaxProposalsPerEpoch:  3,
		CrossoverProbability:  0.25,
		FitnessTemperatureUSD: 25,
		NoveltyWeight:         0.5,
	}
}

func (c ReproductionConfig) withDefaults() ReproductionConfig {
	d := DefaultReproductionConfig()
	if c.MaxProposalsPerEpoch <= 0 {
		c.MaxProposalsPerEpoch = d.MaxProposalsPerEpoch
	}
	if c.CrossoverProbability <= 0 {
		c.CrossoverProbability = d.CrossoverProbability
	}
	if c.FitnessTemperatureUSD <= 0 {
		c.FitnessTemperatureUSD = d.FitnessTemperatureUSD
	}
	if c.NoveltyWeight <= 0 {
		c.NoveltyWeight = d.NoveltyWeight
	}
	return c
}

// Elite is the best-performing agent in one archive niche.
type Elite struct {
	AgentID  string
	Niche    string
	Fitness  float64
	Genome   agent.AgentGenome
	Snapshot AgentSnapshot
}

// EliteArchive is a MAP-Elites style archive: one elite per
// (task_type, chain, model) niche, keyed by tripleKey.
type EliteArchive map[string]Elite

// BuildEliteArchive picks, per niche, the live agent with the highest
// window net profit. Dead agents and agents without a genome are
// skipped. Ties are broken by AgentID so the archive is deterministic.
func BuildEliteArchive(state EpochState) EliteArchive {
	archive := EliteArchive{}
	for _, snap := range state.Snapshots {
		if snap.Status == "dead" {
			continue
		}
		g, ok := state.Genomes[snap.AgentID]
		if !ok {
			continue
		}
		niche := tripleKey(g.TaskType, g.Chain, g.StrategistModel)
		cand := Elite{
			AgentID:  snap.AgentID,
			Niche:    niche,
			Fitness:  snap.NetProfitWindowUSD,
			Genome:   g,
			Snapshot: snap,
		}
		cur, exists := archive[niche]
		if !exists || cand.Fitness > cur.Fitness ||
			(cand.Fitness == cur.Fitness && cand.AgentID < cur.AgentID) {
			archive[niche] = cand
		}
	}
	return archive
}

// Niches returns the archive keys in sorted order.
func (a EliteArchive) Niches() []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReproductionEligible applies the spec's Reproduce rule (line 523):
// active, funded, positive realized net profit, no unpaid debt, able to
// fully pre-fund offspring, and inside the genome's own
// ReproductionPolicy gates. Returns the blocking reason or "".
func ReproductionEligible(snap AgentSnapshot, g agent.AgentGenome, budgetTight bool) string {
	switch {
	case budgetTight:
		return "budget_tight"
	case snap.Status != "active":
		return "not_active"
	case snap.NodeClass != "funded":
		return "not_funded"
	case snap.NetProfitWindowUSD <= 0:
		return "no_net_profit"
	case snap.OperatingDebtUSD > 0:
		return "unpaid_debt"
	case snap.ProfitableEpochs < g.ReproductionPolicy.MinProfitableEpochs:
		return "too_few_profitable_epochs"
	case snap.NetProfitWindowUSD < g.ReproductionPolicy.MinRealizedNetProfitUSD:
		return "below_min_net_profit"
	case snap.NetProfitWindowUSD < reproductionReserve(g):
		return "cannot_prefund_offspring"
	}
	return ""
}

func reproductionReserve(g agent.AgentGenome) float64 {
	rp := g.ReproductionPolicy
	return rp.OffspringSeedCapitalUSD + rp.OffspringAPIReserveUSD + rp.OffspringFailureBufferUSD
}

// ParentWeight is the DGM-style sampling weight described on
// ReproductionConfig. medianFitness centers the sigmoid so a swarm
// where every agent is profitable still discriminates between them.
func ParentWeight(cfg ReproductionConfig, snap AgentSnapshot, g agent.AgentGenome, swarm []agent.AgentGenome, medianFitness float64) float64 {
	cfg = cfg.withDefaults()
	perf := sigmoid((snap.NetProfitWindowUSD - medianFitness) / cfg.FitnessTemperatureUSD)
	stepping := 1.0 / (1.0 + float64(snap.ChildrenCount))
	novelty := 1.0 + cfg.NoveltyWeight*DiversityBonus(g, swarm)
	return perf * stepping * novelty
}

func sigmoid(x float64) float64 { return 1.0 / (1.0 + math.Exp(-x)) }

// modulePerformanceFor derives the per-module fitness signals Crossover
// consumes from one snapshot.
func modulePerformanceFor(snap AgentSnapshot) ModulePerformance {
	llmCost := snap.LLMCostWindowUSD
	if llmCost < 0.01 {
		llmCost = 0.01
	}
	econ := 0.0
	if snap.OperatingDebtUSD == 0 && snap.NetProfitWindowUSD > 0 {
		econ = 1.0 + snap.BalanceUSD
	}
	return ModulePerformance{
		TaskPnL:               snap.NetProfitWindowUSD,
		BrainEfficiency:       snap.NetProfitWindowUSD / llmCost,
		EconomicsHealth:       econ,
		CommunicationAccuracy: snap.SignalAccuracy,
	}
}

// ProposeOffspring is the "identify crossover opportunities" step of
// the epoch flow (spec line 487). It builds the elite archive, samples
// up to MaxProposalsPerEpoch eligible parents by ParentWeight without
// replacement, and produces one candidate genome per parent via
// mutation or (with CrossoverProbability, when another niche's elite
// exists) module-level crossover. Learned rules are inherited with
// confidence regression (agent.Inherit).
//
// Parents that already have a proposal pending in state are skipped so
// one lineage can't flood the queue. Returns nil when the budget is
// tight (spec: "freeze offspring") or no parent is eligible.
//
// Deterministic given rng.
func ProposeOffspring(rng *rand.Rand, cfg ReproductionConfig, state EpochState) []OffspringProposal {
	cfg = cfg.withDefaults()
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	if state.BudgetTight {
		return nil
	}

	pending := map[string]bool{}
	for _, p := range state.OffspringProposals {
		pending[p.ParentID] = true
	}

	type candidate struct {
		snap   AgentSnapshot
		genome agent.AgentGenome
		weight float64
	}
	var (
		cands     []candidate
		fitnesses []float64
	)
	for _, snap := range state.Snapshots {
		if snap.Status != "dead" {
			fitnesses = append(fitnesses, snap.NetProfitWindowUSD)
		}
	}
	median := medianOf(fitnesses)
	for _, snap := range state.Snapshots {
		g, ok := state.Genomes[snap.AgentID]
		if !ok || pending[snap.AgentID] {
			continue
		}
		if ReproductionEligible(snap, g, state.BudgetTight) != "" {
			continue
		}
		cands = append(cands, candidate{
			snap:   snap,
			genome: g,
			weight: ParentWeight(cfg, snap, g, state.SwarmGenomes, median),
		})
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].snap.AgentID < cands[j].snap.AgentID })

	archive := BuildEliteArchive(state)
	var out []OffspringProposal
	for len(cands) > 0 && len(out) < cfg.MaxProposalsPerEpoch {
		idx := weightedIndex(rng, func(i int) float64 { return cands[i].weight }, len(cands))
		c := cands[idx]
		cands = append(cands[:idx], cands[idx+1:]...)
		out = append(out, proposeFrom(rng, cfg, c.snap, c.genome, c.weight, archive, state.EpochID))
	}
	return out
}

func proposeFrom(rng *rand.Rand, cfg ReproductionConfig, snap AgentSnapshot, parent agent.AgentGenome, weight float64, archive EliteArchive, epochID string) OffspringProposal {
	parentNiche := tripleKey(parent.TaskType, parent.Chain, parent.StrategistModel)
	prop := OffspringProposal{
		ProposalID:             fmt.Sprintf("prop-%s-%08x", snap.AgentID, rng.Uint32()),
		ParentID:               snap.AgentID,
		EpochID:                epochID,
		ReproductionReserveUSD: reproductionReserve(parent),
		EvolutionMethod:        EvolutionMutation,
	}

	// Crossover partner: an elite from a different niche, sampled by
	// fitness rank so weak niches still occasionally contribute.
	var partner *Elite
	if rng.Float64() < cfg.CrossoverProbability {
		var others []Elite
		for _, niche := range archive.Niches() {
			e := archive[niche]
			if niche != parentNiche && e.AgentID != snap.AgentID {
				others = append(others, e)
			}
		}
		if len(others) > 0 {
			idx := weightedIndex(rng, func(i int) float64 {
				return sigmoid(others[i].Fitness / cfg.FitnessTemperatureUSD)
			}, len(others))
			partner = &others[idx]
		}
	}

	var child agent.AgentGenome
	if partner != nil {
		crossed, inh, err := Crossover(rng, parent, partner.Genome, modulePerformanceFor(snap), modulePerformanceFor(partner.Snapshot))
		if err == nil {
			child = crossed
			prop.EvolutionMethod = EvolutionCrossover
			prop.SecondParentID = partner.AgentID
			prop.Inheritance = &inh
		}
	}
	if prop.EvolutionMethod == EvolutionMutation {
		child = Mutate(rng, parent)
	}

	child.LearnedRules = agent.Inherit(child.LearnedRules)
	child.Name = fmt.Sprintf("%s-g%d-%04x", parent.Name, child.Generation, rng.Uint32()&0xffff)
	prop.Candidate = child
	prop.Rationale = rationaleFor(prop, snap, parentNiche, weight, partner)
	return prop
}

func rationaleFor(prop OffspringProposal, snap AgentSnapshot, niche string, weight float64, partner *Elite) string {
	base := fmt.Sprintf(
		"archive parent %s (niche %s, net_profit=%.2f USD, children=%d, sample_weight=%.3f)",
		snap.AgentID, niche, snap.NetProfitWindowUSD, snap.ChildrenCount, weight,
	)
	if partner == nil {
		return base + "; method=mutation"
	}
	return fmt.Sprintf("%s; method=crossover with niche elite %s (%s, net_profit=%.2f USD) modules task=%s brain=%s econ=%s comms=%s",
		base, partner.AgentID, partner.Niche, partner.Fitness,
		prop.Inheritance.Task, prop.Inheritance.Brain, prop.Inheritance.Economics, prop.Inheritance.Communication)
}

// weightedIndex samples i in [0, n) with probability ∝ weight(i).
// Falls back to uniform when every weight is zero or non-finite.
func weightedIndex(rng *rand.Rand, weight func(int) float64, n int) int {
	total := 0.0
	for i := 0; i < n; i++ {
		w := weight(i)
		if w > 0 && !math.IsInf(w, 0) && !math.IsNaN(w) {
			total += w
		}
	}
	if total <= 0 {
		return rng.IntN(n)
	}
	target := rng.Float64() * total
	acc := 0.0
	for i := 0; i < n; i++ {
		w := weight(i)
		if w <= 0 || math.IsInf(w, 0) || math.IsNaN(w) {
			continue
		}
		acc += w
		if target < acc {
			return i
		}
	}
	return n - 1
}

func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
