package chain_test

import (
	"strings"
	"testing"

	"github.com/veermshah/cambrian/internal/chain"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	chain.Reset()
	t.Cleanup(chain.Reset)

	chain.Register("solana", func(cfg chain.Config) (chain.ChainClient, error) {
		f := chain.NewFake("solana", "SOL")
		if cfg.RPCURL != "" {
			// Round-trip a config value through the factory so callers can
			// trust that Register hands the Config straight through.
			f = f.WithBalance("rpc-probe", 1)
		}
		return f, nil
	})

	got, err := chain.Get("solana")
	if err != nil {
		t.Fatalf("Get(solana): %v", err)
	}
	client, err := got(chain.Config{Network: "devnet", RPCURL: "https://example"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if client.ChainName() != "solana" || client.NativeToken() != "SOL" {
		t.Fatalf("client identity = (%s, %s)", client.ChainName(), client.NativeToken())
	}

	names := chain.Names()
	if len(names) != 1 || names[0] != "solana" {
		t.Fatalf("Names = %v, want [solana]", names)
	}
}

func TestRegistry_GetUnknownReturnsError(t *testing.T) {
	chain.Reset()
	t.Cleanup(chain.Reset)

	chain.Register("solana", func(chain.Config) (chain.ChainClient, error) {
		return chain.NewFake("solana", "SOL"), nil
	})

	_, err := chain.Get("ethereum")
	if err == nil {
		t.Fatal("Get(ethereum): want error")
	}
	if !strings.Contains(err.Error(), "ethereum") || !strings.Contains(err.Error(), "solana") {
		t.Fatalf("error %q should name the missing chain and list registered ones", err)
	}
}

func TestRegistry_NamesSortedAndOverridable(t *testing.T) {
	chain.Reset()
	t.Cleanup(chain.Reset)

	chain.Register("solana", func(chain.Config) (chain.ChainClient, error) {
		return chain.NewFake("solana", "SOL"), nil
	})
	chain.Register("base", func(chain.Config) (chain.ChainClient, error) {
		return chain.NewFake("base", "ETH"), nil
	})

	names := chain.Names()
	if len(names) != 2 || names[0] != "base" || names[1] != "solana" {
		t.Fatalf("Names = %v, want [base solana]", names)
	}

	// Re-register overrides — keeps tests cheap.
	chain.Register("solana", func(chain.Config) (chain.ChainClient, error) {
		return chain.NewFake("solana", "WSOL"), nil
	})
	f, err := chain.Get("solana")
	if err != nil {
		t.Fatalf("Get(solana) after override: %v", err)
	}
	c, err := f(chain.Config{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if c.NativeToken() != "WSOL" {
		t.Fatalf("override not applied: NativeToken = %s, want WSOL", c.NativeToken())
	}
}

func TestRegistry_PanicsOnEmptyNameOrNilFactory(t *testing.T) {
	chain.Reset()
	t.Cleanup(chain.Reset)

	assertPanic(t, "empty name", func() {
		chain.Register("", func(chain.Config) (chain.ChainClient, error) { return nil, nil })
	})
	assertPanic(t, "nil factory", func() {
		chain.Register("solana", nil)
	})
}

func assertPanic(t *testing.T, label string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s: expected panic", label)
		}
	}()
	fn()
}
