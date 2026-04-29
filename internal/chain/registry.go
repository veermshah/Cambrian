package chain

import (
	"fmt"
	"sort"
	"sync"
)

// Config is the bag of values a ChainClient factory needs to build a
// concrete client. Implementations read what they need; the rest is
// ignored. RPC URL is the chain-specific endpoint (Helius for Solana,
// Alchemy for Base); Network selects between dev and mainnet variants.
type Config struct {
	Network string            // "devnet", "mainnet", "sepolia"
	RPCURL  string            // chain-specific JSON-RPC / HTTP endpoint
	Extra   map[string]string // optional overrides (Jupiter URL, 1inch key, ...)
}

// Factory builds a ChainClient from the given Config.
type Factory func(cfg Config) (ChainClient, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register binds a chain name to a factory. Idempotent: re-registering a
// name overrides the previous factory, which keeps tests simple.
func Register(name string, f Factory) {
	if name == "" {
		panic("chain.Register: empty name")
	}
	if f == nil {
		panic("chain.Register: nil factory")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Get returns the factory for the given chain name.
func Get(name string) (Factory, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("chain: no factory registered for %q (registered: %v)", name, registeredLocked())
	}
	return f, nil
}

// Names returns every registered chain name, sorted for deterministic
// output in logs and tests.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registeredLocked()
}

func registeredLocked() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Reset clears the registry. Tests use it to start from a known-empty
// state; production code should not call it.
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Factory{}
}
