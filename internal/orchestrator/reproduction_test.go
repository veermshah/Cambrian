package orchestrator

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/veermshah/cambrian/internal/agent"
)

func eligibleGenome(name, task, chain, model string) agent.AgentGenome {
	g := parentA()
	g.Name = name
	g.TaskType = task
	g.Chain = chain
	g.StrategistModel = model
	g.ReproductionPolicy = agent.ReproductionPolicy{
		MinProfitableEpochs:       2,
		MinRealizedNetProfitUSD:   10,
		OffspringSeedCapitalUSD:   20,
		OffspringAPIReserveUSD:    5,
		OffspringFailureBufferUSD: 5,
	}
	return g
}

func eligibleSnap(id string, profit float64) AgentSnapshot {
	return AgentSnapshot{
		AgentID:            id,
		Status:             "active",
		NodeClass:          "funded",
		NetProfitWindowUSD: profit,
		ProfitableEpochs:   5,
		BalanceUSD:         100,
		LLMCostWindowUSD:   1,
		SignalAccuracy:     0.5,
	}
}

func reproState() EpochState {
	st := EpochState{
		EpochID: "epoch-1",
		Genomes: map[string]agent.AgentGenome{},
	}
	add := func(id string, profit float64, g agent.AgentGenome) {
		st.Snapshots = append(st.Snapshots, eligibleSnap(id, profit))
		st.Genomes[id] = g
		st.SwarmGenomes = append(st.SwarmGenomes, g)
	}
	add("a1", 120, eligibleGenome("a1", "cross_chain_yield", "solana", "claude-haiku-4-5-20251001"))
	add("a2", 80, eligibleGenome("a2", "cross_chain_yield", "solana", "claude-haiku-4-5-20251001"))
	add("b1", 60, eligibleGenome("b1", "momentum", "base", "claude-sonnet-4-6"))
	add("c1", 40, eligibleGenome("c1", "lending", "base", "claude-haiku-4-5-20251001"))
	return st
}

func TestBuildEliteArchive_OnePerNicheBestFitness(t *testing.T) {
	st := reproState()
	dead := eligibleSnap("z9", 999)
	dead.Status = "dead"
	st.Snapshots = append(st.Snapshots, dead)
	st.Genomes["z9"] = st.Genomes["a1"]

	arch := BuildEliteArchive(st)
	if len(arch) != 3 {
		t.Fatalf("expected 3 niches, got %d: %v", len(arch), arch.Niches())
	}
	e := arch[tripleKey("cross_chain_yield", "solana", "claude-haiku-4-5-20251001")]
	if e.AgentID != "a1" {
		t.Fatalf("expected a1 as niche elite (dead z9 skipped, a2 lower), got %s", e.AgentID)
	}
}

func TestReproductionEligible_Gates(t *testing.T) {
	g := eligibleGenome("x", "momentum", "base", "m")
	cases := []struct {
		name  string
		mut   func(*AgentSnapshot)
		tight bool
		want  string
	}{
		{"ok", func(*AgentSnapshot) {}, false, ""},
		{"budget", func(*AgentSnapshot) {}, true, "budget_tight"},
		{"paused", func(s *AgentSnapshot) { s.Status = "paused" }, false, "not_active"},
		{"shadow", func(s *AgentSnapshot) { s.NodeClass = "shadow" }, false, "not_funded"},
		{"loss", func(s *AgentSnapshot) { s.NetProfitWindowUSD = -1 }, false, "no_net_profit"},
		{"debt", func(s *AgentSnapshot) { s.OperatingDebtUSD = 3 }, false, "unpaid_debt"},
		{"epochs", func(s *AgentSnapshot) { s.ProfitableEpochs = 1 }, false, "too_few_profitable_epochs"},
		{"min_profit", func(s *AgentSnapshot) { s.NetProfitWindowUSD = 5 }, false, "below_min_net_profit"},
		{"prefund", func(s *AgentSnapshot) { s.NetProfitWindowUSD = 20 }, false, "cannot_prefund_offspring"},
	}
	for _, tc := range cases {
		s := eligibleSnap("x", 100)
		tc.mut(&s)
		if got := ReproductionEligible(s, g, tc.tight); got != tc.want {
			t.Errorf("%s: want %q got %q", tc.name, tc.want, got)
		}
	}
}

func TestParentWeight_PrefersFitterFewerChildrenRarerNiche(t *testing.T) {
	st := reproState()
	cfg := DefaultReproductionConfig()
	g := st.Genomes["a1"]

	rich := eligibleSnap("a1", 200)
	poor := eligibleSnap("a1", 20)
	if ParentWeight(cfg, rich, g, st.SwarmGenomes, 60) <= ParentWeight(cfg, poor, g, st.SwarmGenomes, 60) {
		t.Fatal("higher fitness should weigh more")
	}

	kids := rich
	kids.ChildrenCount = 4
	if ParentWeight(cfg, kids, g, st.SwarmGenomes, 60) >= ParentWeight(cfg, rich, g, st.SwarmGenomes, 60) {
		t.Fatal("stepping-stone bonus: more children should weigh less")
	}

	common := st.Genomes["a1"] // shares niche with a2
	rare := st.Genomes["b1"]   // unique niche
	if ParentWeight(cfg, rich, rare, st.SwarmGenomes, 60) <= ParentWeight(cfg, rich, common, st.SwarmGenomes, 60) {
		t.Fatal("rarer niche should receive a novelty bonus")
	}
}

func TestProposeOffspring_BudgetTightFreezes(t *testing.T) {
	st := reproState()
	st.BudgetTight = true
	if got := ProposeOffspring(rand.New(rand.NewPCG(1, 1)), DefaultReproductionConfig(), st); got != nil {
		t.Fatalf("expected no proposals under tight budget, got %d", len(got))
	}
}

func TestProposeOffspring_CapAndNoDuplicateParents(t *testing.T) {
	st := reproState()
	st.OffspringProposals = []OffspringProposal{{ProposalID: "pending", ParentID: "a1"}}
	cfg := DefaultReproductionConfig()
	cfg.MaxProposalsPerEpoch = 2

	props := ProposeOffspring(rand.New(rand.NewPCG(7, 7)), cfg, st)
	if len(props) != 2 {
		t.Fatalf("expected cap of 2, got %d", len(props))
	}
	seen := map[string]bool{}
	for _, p := range props {
		if p.ParentID == "a1" {
			t.Fatal("parent with pending proposal must be skipped")
		}
		if seen[p.ParentID] {
			t.Fatalf("parent %s selected twice", p.ParentID)
		}
		seen[p.ParentID] = true
		if p.EpochID != "epoch-1" || p.ReproductionReserveUSD != 30 || p.Rationale == "" || p.Candidate.Name == "" {
			t.Fatalf("proposal missing provenance: %+v", p)
		}
		if p.Candidate.Generation <= st.Genomes[p.ParentID].Generation {
			t.Fatal("child generation must advance")
		}
	}
}

func TestProposeOffspring_MutationOnlyWhenCrossoverDisabled(t *testing.T) {
	st := reproState()
	cfg := DefaultReproductionConfig()
	cfg.CrossoverProbability = 1e-12 // withDefaults keeps >0 values
	for _, p := range ProposeOffspring(rand.New(rand.NewPCG(3, 3)), cfg, st) {
		if p.EvolutionMethod != EvolutionMutation || p.SecondParentID != "" || p.Inheritance != nil {
			t.Fatalf("expected pure mutation, got %+v", p)
		}
	}
}

func TestProposeOffspring_CrossoverUsesOtherNicheElite(t *testing.T) {
	st := reproState()
	cfg := DefaultReproductionConfig()
	cfg.CrossoverProbability = 0.999999
	cfg.MaxProposalsPerEpoch = 4

	props := ProposeOffspring(rand.New(rand.NewPCG(5, 5)), cfg, st)
	if len(props) != 4 {
		t.Fatalf("expected 4 proposals, got %d", len(props))
	}
	for _, p := range props {
		if p.EvolutionMethod != EvolutionCrossover {
			t.Fatalf("expected crossover, got %s", p.EvolutionMethod)
		}
		if p.SecondParentID == "" || p.SecondParentID == p.ParentID || p.Inheritance == nil {
			t.Fatalf("crossover provenance incomplete: %+v", p)
		}
		pg, sg := st.Genomes[p.ParentID], st.Genomes[p.SecondParentID]
		if tripleKey(pg.TaskType, pg.Chain, pg.StrategistModel) == tripleKey(sg.TaskType, sg.Chain, sg.StrategistModel) {
			t.Fatal("partner must come from a different niche")
		}
	}
}

func TestProposeOffspring_InheritsLearnedRulesWithRegression(t *testing.T) {
	st := reproState()
	for id, g := range st.Genomes {
		g.LearnedRules = []agent.LearnedRule{{ID: "r-" + id, Text: "rule " + id, Confidence: 0.95}}
		st.Genomes[id] = g
	}
	cfg := DefaultReproductionConfig()
	cfg.CrossoverProbability = 1e-12
	for _, p := range ProposeOffspring(rand.New(rand.NewPCG(9, 9)), cfg, st) {
		if len(p.Candidate.LearnedRules) != 1 {
			t.Fatalf("expected one inherited rule, got %d", len(p.Candidate.LearnedRules))
		}
		r := p.Candidate.LearnedRules[0]
		if r.ID == "r-"+p.ParentID {
			t.Fatal("inherited rule must get a fresh ID")
		}
		if r.Confidence >= 0.95 {
			t.Fatalf("inherited confidence must regress toward mean, got %f", r.Confidence)
		}
	}
}

func TestProposeOffspring_Deterministic(t *testing.T) {
	st := reproState()
	cfg := DefaultReproductionConfig()
	a := ProposeOffspring(rand.New(rand.NewPCG(42, 42)), cfg, st)
	b := ProposeOffspring(rand.New(rand.NewPCG(42, 42)), cfg, st)
	// Inherited rule IDs come from crypto/rand by design; everything
	// else must match.
	strip := func(ps []OffspringProposal) {
		for i := range ps {
			for j := range ps[i].Candidate.LearnedRules {
				ps[i].Candidate.LearnedRules[j].ID = ""
			}
		}
	}
	strip(a)
	strip(b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed must produce identical proposals")
	}
}

func TestProposeOffspring_NoEligibleReturnsNil(t *testing.T) {
	st := reproState()
	for i := range st.Snapshots {
		st.Snapshots[i].OperatingDebtUSD = 1
	}
	if got := ProposeOffspring(rand.New(rand.NewPCG(1, 1)), DefaultReproductionConfig(), st); got != nil {
		t.Fatalf("expected nil, got %d", len(got))
	}
}
