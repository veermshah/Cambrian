package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// goodGenome is the minimal genome that passes Validate. Every table
// test mutates a copy of this to provoke a single failure.
func goodGenome() AgentGenome {
	return AgentGenome{
		Name:         "alpha-1",
		Generation:   0,
		LineageDepth: 0,
		TaskType:     "cross_chain_yield",
		Chain:        "solana",
		StrategyConfig: map[string]interface{}{
			"min_apy_spread_bps": 50,
		},
		StrategistPrompt:          "you are a yield-seeking agent",
		StrategistModel:           "claude-haiku-4-5-20251001",
		StrategistIntervalSeconds: 300,
		BanditPolicies:            []string{"thompson"},
		LearnedRules:              []LearnedRule{{ID: "r1", Text: "avoid stETH dips", Confidence: 0.7}},
		CapitalAllocation:         0.1,
		ReproductionPolicy: ReproductionPolicy{
			MinProfitableEpochs:       3,
			MinRealizedNetProfitUSD:   100,
			OffspringSeedCapitalUSD:   50,
			OffspringAPIReserveUSD:    10,
			OffspringFailureBufferUSD: 5,
			MaxDescendantsPerEpoch:    1,
		},
		CostPolicy: CostPolicy{
			MonthlyLLMBudgetUSD:       20,
			MonthlyInfraRentBudgetUSD: 5,
			PauseOnBudgetBreach:       true,
		},
		CommunicationPolicy: CommunicationPolicy{
			SubscribeChannels:     []string{"intel.market"},
			PublishChannels:       []string{"intel.scout"},
			MaxBroadcastsPerCycle: 4,
			IntelSummaryMaxItems:  10,
			RequireBullBearTag:    true,
		},
		SleepSchedule: SleepSchedule{
			AwakeWindowMinutes: 15,
			SleepBetween:       true,
			WakeForBacktest:    false,
		},
	}
}

func TestValidateAcceptsCleanGenome(t *testing.T) {
	g := goodGenome()
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateTableDriven(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AgentGenome)
		wantSub string
	}{
		{"empty name", func(g *AgentGenome) { g.Name = "" }, "name"},
		{"negative generation", func(g *AgentGenome) { g.Generation = -1 }, "generation"},
		{"negative lineage depth", func(g *AgentGenome) { g.LineageDepth = -1 }, "lineage_depth"},
		{"unknown task type", func(g *AgentGenome) { g.TaskType = "scalping" }, "task_type"},
		{"unknown chain", func(g *AgentGenome) { g.Chain = "ethereum" }, "chain"},
		{"unknown strategist model", func(g *AgentGenome) { g.StrategistModel = "claude-3" }, "strategist_model"},
		{"negative strategist interval", func(g *AgentGenome) { g.StrategistIntervalSeconds = -1 }, "strategist_interval_seconds"},
		{"capital_allocation > 1", func(g *AgentGenome) { g.CapitalAllocation = 1.5 }, "capital_allocation"},
		{"capital_allocation < 0", func(g *AgentGenome) { g.CapitalAllocation = -0.1 }, "capital_allocation"},
		{"negative min_profitable_epochs", func(g *AgentGenome) { g.ReproductionPolicy.MinProfitableEpochs = -1 }, "min_profitable_epochs"},
		{"negative min_realized_net_profit_usd", func(g *AgentGenome) { g.ReproductionPolicy.MinRealizedNetProfitUSD = -1 }, "min_realized_net_profit_usd"},
		{"negative offspring_seed_capital_usd", func(g *AgentGenome) { g.ReproductionPolicy.OffspringSeedCapitalUSD = -1 }, "offspring_seed_capital_usd"},
		{"negative offspring_api_reserve_usd", func(g *AgentGenome) { g.ReproductionPolicy.OffspringAPIReserveUSD = -1 }, "offspring_api_reserve_usd"},
		{"negative offspring_failure_buffer_usd", func(g *AgentGenome) { g.ReproductionPolicy.OffspringFailureBufferUSD = -1 }, "offspring_failure_buffer_usd"},
		{"negative max_descendants_per_epoch", func(g *AgentGenome) { g.ReproductionPolicy.MaxDescendantsPerEpoch = -1 }, "max_descendants_per_epoch"},
		{"negative monthly_llm_budget_usd", func(g *AgentGenome) { g.CostPolicy.MonthlyLLMBudgetUSD = -1 }, "monthly_llm_budget_usd"},
		{"negative monthly_infra_rent_budget_usd", func(g *AgentGenome) { g.CostPolicy.MonthlyInfraRentBudgetUSD = -1 }, "monthly_infra_rent_budget_usd"},
		{"negative max_broadcasts_per_cycle", func(g *AgentGenome) { g.CommunicationPolicy.MaxBroadcastsPerCycle = -1 }, "max_broadcasts_per_cycle"},
		{"negative intel_summary_max_items", func(g *AgentGenome) { g.CommunicationPolicy.IntelSummaryMaxItems = -1 }, "intel_summary_max_items"},
		{"negative awake_window_minutes", func(g *AgentGenome) { g.SleepSchedule.AwakeWindowMinutes = -1 }, "awake_window_minutes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := goodGenome()
			tc.mutate(&g)
			err := g.Validate()
			if err == nil {
				t.Fatalf("Validate: want error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateNilReceiver(t *testing.T) {
	var g *AgentGenome
	if err := g.Validate(); err == nil {
		t.Fatal("nil genome: want error")
	}
}

// TestJSONRoundTrip confirms the JSON tags match spec lines 577–605
// exactly: marshal → unmarshal → marshal should produce byte-identical
// output, with no fields silently renamed or dropped.
func TestJSONRoundTrip(t *testing.T) {
	g := goodGenome()
	first, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded AgentGenome
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	second, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("round-trip mismatch:\n first: %s\nsecond: %s", first, second)
	}
}

// TestJSONTagsMatchSpec spot-checks a handful of tags that have caused
// subtle bugs before (snake_case vs camelCase). If any of these is
// wrong, the JSONB column in agents.genome won't round-trip.
func TestJSONTagsMatchSpec(t *testing.T) {
	g := goodGenome()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"name":"alpha-1"`,
		`"generation":0`,
		`"lineage_depth":0`,
		`"task_type":"cross_chain_yield"`,
		`"chain":"solana"`,
		`"strategy_config":`,
		`"strategist_prompt":`,
		`"strategist_model":"claude-haiku-4-5-20251001"`,
		`"strategist_interval_seconds":300`,
		`"bandit_policies":`,
		`"learned_rules":`,
		`"capital_allocation":0.1`,
		`"reproduction_policy":`,
		`"cost_policy":`,
		`"communication_policy":`,
		`"sleep_schedule":`,
		`"min_profitable_epochs":3`,
		`"min_realized_net_profit_usd":100`,
		`"offspring_seed_capital_usd":50`,
		`"offspring_api_reserve_usd":10`,
		`"offspring_failure_buffer_usd":5`,
		`"max_descendants_per_epoch":1`,
		`"allow_task_type_mutation":false`,
		`"allow_chain_mutation":false`,
		`"allow_model_mutation":false`,
		`"monthly_llm_budget_usd":20`,
		`"monthly_infra_rent_budget_usd":5`,
		`"pause_on_budget_breach":true`,
		`"subscribe_channels":`,
		`"publish_channels":`,
		`"max_broadcasts_per_cycle":4`,
		`"intel_summary_max_items":10`,
		`"require_bull_bear_tag":true`,
		`"awake_window_minutes":15`,
		`"sleep_between_windows":true`,
		`"wake_for_backtest":false`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing JSON fragment %q in:\n%s", want, s)
		}
	}
}
