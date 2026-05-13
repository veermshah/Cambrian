package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// TaskRegistry holds the factories for every task type the orchestrator
// knows about. The four real tasks (cross_chain_yield, liquidity_provision,
// liquidation_hunting, momentum) register themselves via init() in their
// own files (chunks 11, 12, 26, 27).
//
// Spec lines 249–254 define the canonical map; this implementation
// wraps it in a mutex so registration during init() races (and tests
// that swap factories with Reset) stay safe.
var registry = struct {
	mu        sync.RWMutex
	factories map[string]TaskFactory
}{factories: map[string]TaskFactory{}}

// Register associates name with factory. Panics on duplicate name to
// fail loud at startup if two tasks claim the same slot.
func Register(name string, factory TaskFactory) {
	if name == "" {
		panic("tasks: Register with empty name")
	}
	if factory == nil {
		panic("tasks: Register with nil factory")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.factories[name]; exists {
		panic(fmt.Sprintf("tasks: duplicate registration for %q", name))
	}
	registry.factories[name] = factory
}

// Build looks up the factory for name and constructs a Task with the
// given raw config. Returns an error if name is unregistered.
func Build(ctx context.Context, name string, config json.RawMessage) (Task, error) {
	registry.mu.RLock()
	f, ok := registry.factories[name]
	registry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tasks: unknown task type %q", name)
	}
	return f(ctx, config)
}

// Registered returns the registered task type names in deterministic
// (sorted) order. Used by orchestrator logging and the dashboard.
func Registered() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.factories))
	for name := range registry.factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResetForTest clears the registry. Test-only helper — used by registry
// tests to verify Register's duplicate guard without leaking state into
// neighboring tests.
func ResetForTest() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.factories = map[string]TaskFactory{}
}
