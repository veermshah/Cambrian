package tasks

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRegisterAndBuild(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	seed := FakeTask{Position: 42.5, Summary: map[string]interface{}{"ok": true}}
	Register("fake_one", FakeFactory(seed))

	task, err := Build(context.Background(), "fake_one", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := task.GetPositionValue(context.Background())
	if err != nil {
		t.Fatalf("GetPositionValue: %v", err)
	}
	if got != 42.5 {
		t.Errorf("Position = %v, want 42.5", got)
	}
}

func TestBuildUnknownTaskTypeFails(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	if _, err := Build(context.Background(), "ghost", nil); err == nil {
		t.Error("Build on unknown name: want error")
	}
}

func TestRegisterPanicsOnEmptyName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	defer func() {
		if recover() == nil {
			t.Error("Register(\"\"): expected panic")
		}
	}()
	Register("", FakeFactory(FakeTask{}))
}

func TestRegisterPanicsOnNilFactory(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	defer func() {
		if recover() == nil {
			t.Error("Register(nil factory): expected panic")
		}
	}()
	Register("x", nil)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Register("dup", FakeFactory(FakeTask{}))
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register: expected panic")
		}
	}()
	Register("dup", FakeFactory(FakeTask{}))
}

func TestRegisteredReturnsSortedNames(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Register("zebra", FakeFactory(FakeTask{}))
	Register("alpha", FakeFactory(FakeTask{}))
	Register("mango", FakeFactory(FakeTask{}))
	got := Registered()
	want := []string{"alpha", "mango", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Registered = %v, want %v", got, want)
	}
}

func TestFakeTaskSatisfiesInterface(t *testing.T) {
	// Compile-time check via var, runtime check by exercising every
	// method so the call counts increment and the test can assert.
	ft := &FakeTask{
		TickTrades: []Trade{{Chain: "solana", TradeType: "swap"}},
		Position:   12.5,
	}
	var task Task = ft

	trades, err := task.RunTick(context.Background())
	if err != nil || len(trades) != 1 {
		t.Errorf("RunTick: trades=%v err=%v", trades, err)
	}
	if _, err := task.GetStateSummary(context.Background()); err != nil {
		t.Errorf("GetStateSummary: %v", err)
	}
	if err := task.ApplyAdjustments(map[string]interface{}{"k": 1}); err != nil {
		t.Errorf("ApplyAdjustments: %v", err)
	}
	if ft.LastAdjustments["k"] != 1 {
		t.Errorf("ApplyAdjustments did not record args")
	}
	pos, _ := task.GetPositionValue(context.Background())
	if pos != 12.5 {
		t.Errorf("GetPositionValue = %v", pos)
	}
	if _, err := task.CloseAllPositions(context.Background()); err != nil {
		t.Errorf("CloseAllPositions: %v", err)
	}
	if ft.RunTickCallCount != 1 || ft.CloseAllCallCount != 1 {
		t.Errorf("call counts: tick=%d close=%d", ft.RunTickCallCount, ft.CloseAllCallCount)
	}
}
