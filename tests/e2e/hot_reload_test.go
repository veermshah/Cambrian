package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/veermshah/cambrian/internal/agent/tasks"
)

// TestHotReload_TaskRegistryAcceptsNewTypeWithoutInterruption — spec line 1197.
// Pure-logic. The chunk-11 TaskRegistry uses Register/Build; a new task
// type registered at runtime is visible to subsequent Build calls
// without the swarm having to restart. This proves the registry path
// is hot-reloadable in principle.
func TestHotReload_TaskRegistryAcceptsNewTypeWithoutInterruption(t *testing.T) {
	// Build a dummy task type. Register/Build is goroutine-safe per the
	// registry's sync.RWMutex.
	const newType = "e2e-hot-reload-task"
	tasks.Register(newType, func(_ context.Context, _ json.RawMessage) (tasks.Task, error) {
		return nopTask{}, nil
	})
	defer func() {
		// No public Deregister — registry overrides instead, which is
		// fine because the registry tests already cover overwrite
		// semantics. We leave the registration in place; package init
		// is idempotent for the canonical types.
	}()

	task, err := tasks.Build(context.Background(), newType, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Error("task is nil")
	}
}

// nopTask satisfies the tasks.Task interface with no behavior.
type nopTask struct{}

func (nopTask) RunTick(context.Context) ([]tasks.Trade, error)             { return nil, nil }
func (nopTask) GetStateSummary(context.Context) (map[string]any, error)    { return nil, nil }
func (nopTask) ApplyAdjustments(map[string]any) error                      { return nil }
func (nopTask) GetPositionValue(context.Context) (float64, error)          { return 0, nil }
func (nopTask) CloseAllPositions(context.Context) ([]tasks.Trade, error)   { return nil, nil }
