package orchestrator

import (
	"testing"
	"time"
)

func healthySnapshot() AgentSnapshot {
	return AgentSnapshot{
		AgentID:                 "agent-1",
		Status:                  "active",
		BalanceUSD:              100.0,
		Drawdown:                0.05,
		ConsecutiveLosingEpochs: 0,
		OperatingDebtUSD:        0,
		LastHeartbeat:           time.Now(),
		LastStrategistVerdict:   "continue",
		NetProfitWindowUSD:      5.0,
		NodeClass:               "funded",
	}
}

func TestPolicy_HealthyAgentNoAction(t *testing.T) {
	now := time.Now()
	actions := DeterministicLifecyclePolicy(now, LifecyclePolicyConfig{}, []AgentSnapshot{healthySnapshot()}, false)
	if len(actions) != 0 {
		t.Errorf("healthy agent produced actions: %+v", actions)
	}
}

func TestPolicy_KillOnLowBalance(t *testing.T) {
	s := healthySnapshot()
	s.BalanceUSD = 0.5
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Kind != ActionKill || actions[0].Reason != "balance_below_minimum" {
		t.Errorf("expected kill/balance_below_minimum, got %+v", actions)
	}
}

func TestPolicy_KillOnDrawdown(t *testing.T) {
	s := healthySnapshot()
	s.Drawdown = 0.45
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Reason != "max_drawdown_exceeded" {
		t.Errorf("expected drawdown kill, got %+v", actions)
	}
}

func TestPolicy_KillOnConsecutiveLosingEpochs(t *testing.T) {
	s := healthySnapshot()
	s.ConsecutiveLosingEpochs = 5
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Reason != "consecutive_losing_epochs" {
		t.Errorf("expected losing-streak kill, got %+v", actions)
	}
}

func TestPolicy_KillOnDebt(t *testing.T) {
	s := healthySnapshot()
	s.OperatingDebtUSD = 10.0
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Reason != "operating_debt_exceeded" {
		t.Errorf("expected debt kill, got %+v", actions)
	}
}

func TestPolicy_KillOnHeartbeatMissing(t *testing.T) {
	now := time.Now()
	s := healthySnapshot()
	s.LastHeartbeat = now.Add(-30 * time.Minute)
	actions := DeterministicLifecyclePolicy(now, LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Reason != "heartbeat_missing" {
		t.Errorf("expected heartbeat kill, got %+v", actions)
	}
}

func TestPolicy_KillOverridesPause(t *testing.T) {
	// Agent is also strategy-pause-worthy, but balance kills first.
	s := healthySnapshot()
	s.BalanceUSD = 0.5
	s.LastStrategistVerdict = "pause"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Kind != ActionKill {
		t.Errorf("kill must beat pause, got %+v", actions)
	}
}

func TestPolicy_PauseOnStrategyMismatch(t *testing.T) {
	s := healthySnapshot()
	s.LastStrategistVerdict = "pause"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Kind != ActionPause || actions[0].Reason != "strategy_regime_mismatch" {
		t.Errorf("expected strategy_regime_mismatch pause, got %+v", actions)
	}
}

func TestPolicy_PauseOnBudgetTightShadow(t *testing.T) {
	s := healthySnapshot()
	s.NodeClass = "shadow"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, true)
	if len(actions) != 1 || actions[0].Reason != "budget_tight" {
		t.Errorf("expected budget_tight pause for shadow, got %+v", actions)
	}
}

func TestPolicy_FundedSurvivesBudgetTight(t *testing.T) {
	s := healthySnapshot() // funded
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, true)
	if len(actions) != 0 {
		t.Errorf("funded should survive budget_tight, got %+v", actions)
	}
}

func TestPolicy_PauseOnSolventButWeak(t *testing.T) {
	s := healthySnapshot()
	s.NetProfitWindowUSD = 0.01
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Reason != "solvent_but_weak" {
		t.Errorf("expected solvent_but_weak pause, got %+v", actions)
	}
}

func TestPolicy_ResumeHealthyPausedAgent(t *testing.T) {
	s := healthySnapshot()
	s.Status = "paused"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 1 || actions[0].Kind != ActionResume {
		t.Errorf("expected resume, got %+v", actions)
	}
}

func TestPolicy_PausedAgentStillPauseWorthyNoAction(t *testing.T) {
	s := healthySnapshot()
	s.Status = "paused"
	s.LastStrategistVerdict = "pause"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 0 {
		t.Errorf("paused + still pause-worthy → no-op, got %+v", actions)
	}
}

func TestPolicy_DeadAgentSkipped(t *testing.T) {
	s := healthySnapshot()
	s.Status = "dead"
	s.BalanceUSD = 0
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	if len(actions) != 0 {
		t.Errorf("dead agent should produce no action, got %+v", actions)
	}
}

func TestPolicy_DefensiveOnlyPausesShadow(t *testing.T) {
	shadow := healthySnapshot()
	shadow.AgentID = "shadow-1"
	shadow.NodeClass = "shadow"
	shadow.LastStrategistVerdict = "defensive"
	funded := healthySnapshot()
	funded.AgentID = "funded-1"
	funded.LastStrategistVerdict = "defensive"
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{shadow, funded}, false)
	gotShadowPause, gotFundedAction := false, false
	for _, a := range actions {
		if a.AgentID == shadow.AgentID && a.Kind == ActionPause && a.Reason == "strategy_defensive" {
			gotShadowPause = true
		}
		if a.AgentID == funded.AgentID {
			gotFundedAction = true
		}
	}
	if !gotShadowPause {
		t.Errorf("shadow defensive should pause: %+v", actions)
	}
	if gotFundedAction {
		t.Errorf("funded defensive should NOT pause: %+v", actions)
	}
}

func TestPolicy_HeartbeatZeroSkipped(t *testing.T) {
	// Newly spawned agent with no heartbeat yet — should not be killed
	// because LastHeartbeat is zero (sentinel).
	s := healthySnapshot()
	s.LastHeartbeat = time.Time{}
	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{s}, false)
	for _, a := range actions {
		if a.Reason == "heartbeat_missing" {
			t.Errorf("zero heartbeat should not trip kill: %+v", a)
		}
	}
}

func TestPolicy_MultipleAgentsIndependent(t *testing.T) {
	kill := healthySnapshot()
	kill.AgentID = "kill-me"
	kill.BalanceUSD = 0
	pause := healthySnapshot()
	pause.AgentID = "pause-me"
	pause.LastStrategistVerdict = "pause"
	healthy := healthySnapshot()
	healthy.AgentID = "healthy"

	actions := DeterministicLifecyclePolicy(time.Now(), LifecyclePolicyConfig{}, []AgentSnapshot{kill, pause, healthy}, false)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions (1 kill + 1 pause), got %d: %+v", len(actions), actions)
	}
	kinds := map[LifecycleActionKind]string{}
	for _, a := range actions {
		kinds[a.Kind] = a.AgentID
	}
	if kinds[ActionKill] != "kill-me" {
		t.Errorf("kill targeted wrong agent: %+v", actions)
	}
	if kinds[ActionPause] != "pause-me" {
		t.Errorf("pause targeted wrong agent: %+v", actions)
	}
}
