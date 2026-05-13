package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veermshah/cambrian/internal/chain"
)

// CrossChainYieldDeps bundles the runtime dependencies the task needs at
// construction time. The Clients map is keyed by chain name ("solana",
// "base") and supplies the ChainClient used to read yield rates and
// (when chunks 4 / 5 connect them) execute protocol entries / exits.
//
// Wallets is keyed the same way. Now is overridable for deterministic
// tests; production callers should pass time.Now or leave it nil to get
// the default.
type CrossChainYieldDeps struct {
	Clients map[string]chain.ChainClient
	Wallets map[string]*chain.Wallet
	Now     func() time.Time
}

// CrossChainYield implements the cross_chain_yield task. Spec lines
// 257–282. It samples APYs across both chains on its own
// CheckIntervalSecs cadence, picks the highest risk-adjusted yield
// among AllowedProtocols, and rebalances no more often than
// RebalanceIntervalSecs.
//
// Bridge cost is modeled (the real bridge integration is deferred — see
// chunk 11 "Out of scope"); a cross-chain rebalance whose estimated cost
// exceeds BridgeCostThreshold is suppressed.
type CrossChainYield struct {
	cfg  CrossChainYieldConfig
	deps CrossChainYieldDeps

	mu sync.Mutex

	// allocation tracks current capital deployed per protocol, in USD.
	// On boot the map is empty — the first rebalance opens positions.
	allocation map[string]positionState

	// freeCapital is uninvested capital held in a wallet, in USD. The
	// runtime seeds this on spawn (chunk 14); tests stage it directly.
	freeCapital float64

	lastCheck     time.Time
	lastRebalance time.Time
	lastRates     []chain.YieldRate

	rebalanceHistory []rebalanceEvent
}

type positionState struct {
	Chain    string
	Asset    string
	ValueUSD float64
	APY      float64
	OpenedAt time.Time
}

type rebalanceEvent struct {
	At         time.Time
	FromProto  string
	ToProto    string
	CapitalUSD float64
	BridgeCost float64
	DiffBps    float64
}

var _ Task = (*CrossChainYield)(nil)

// NewCrossChainYield builds a task with the supplied deps and config.
// Used directly by tests (with chain.FakeChainClient) and indirectly by
// the registered factory once the runtime has called
// SetCrossChainYieldDeps.
func NewCrossChainYield(deps CrossChainYieldDeps, cfg CrossChainYieldConfig) (*CrossChainYield, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Clients == nil {
		return nil, errors.New("cross_chain_yield: deps.Clients required")
	}
	if _, ok := deps.Clients[cfg.PrimaryChain]; !ok {
		return nil, fmt.Errorf("cross_chain_yield: primary chain %q missing from deps.Clients", cfg.PrimaryChain)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &CrossChainYield{
		cfg:        cfg,
		deps:       deps,
		allocation: map[string]positionState{},
	}, nil
}

// SeedFreeCapital sets the USD capital available for the next rebalance.
// The runtime (chunk 14) calls this when wiring the task into a
// NodeRunner; tests call it directly.
func (c *CrossChainYield) SeedFreeCapital(usd float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.freeCapital = usd
}

// --- Task interface --------------------------------------------------------

// RunTick is the fast loop. It throttles its own work by
// CheckIntervalSecs so the NodeRunner can call it on a tight cadence
// without amplifying RPC load.
func (c *CrossChainYield) RunTick(ctx context.Context) ([]Trade, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.deps.Now()

	// 1. Throttle yield-rate polling.
	if !c.lastCheck.IsZero() && now.Sub(c.lastCheck) < time.Duration(c.cfg.CheckIntervalSecs*float64(time.Second)) {
		return nil, nil
	}
	c.lastCheck = now

	// 2. Bail if we have nothing to deploy.
	total := c.totalCapitalLocked()
	if total < c.cfg.MinCapitalToOperate {
		return nil, nil
	}

	// 3. Fetch rates from every chain we have a client for.
	rates, err := c.fetchAllowedRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("cross_chain_yield: fetch rates: %w", err)
	}
	c.lastRates = rates
	if len(rates) == 0 {
		return nil, nil
	}

	// 4. Throttle rebalances independently of rate polling.
	if !c.lastRebalance.IsZero() && now.Sub(c.lastRebalance) < time.Duration(c.cfg.RebalanceIntervalSecs)*time.Second {
		return nil, nil
	}

	// 5. Decide.
	target := c.chooseTarget(rates)
	if len(target) == 0 {
		return nil, nil
	}
	curAPY := c.currentBlendedAPY()
	targetAPY := blendedAPY(target)
	diffBps := (targetAPY - curAPY) * 10000.0
	if diffBps < c.cfg.MinYieldDiffBps {
		return nil, nil
	}

	bridgeCost := c.estimateBridgeCost(target)
	if bridgeCost > c.cfg.BridgeCostThreshold {
		return nil, nil
	}

	// 6. Execute.
	trades := c.rebalance(now, total, target, bridgeCost, diffBps)
	return trades, nil
}

func (c *CrossChainYield) GetStateSummary(_ context.Context) (map[string]interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// top 3 rates per chain
	byChain := map[string][]chain.YieldRate{}
	for _, r := range c.lastRates {
		byChain[r.Chain] = append(byChain[r.Chain], r)
	}
	topPerChain := map[string][]map[string]interface{}{}
	for ch, rs := range byChain {
		sort.Slice(rs, func(i, j int) bool { return rs[i].APY > rs[j].APY })
		if len(rs) > 3 {
			rs = rs[:3]
		}
		out := make([]map[string]interface{}, 0, len(rs))
		for _, r := range rs {
			out = append(out, map[string]interface{}{
				"protocol": r.Protocol,
				"asset":    r.Asset,
				"apy":      r.APY,
			})
		}
		topPerChain[ch] = out
	}

	alloc := make(map[string]map[string]interface{}, len(c.allocation))
	for proto, p := range c.allocation {
		alloc[proto] = map[string]interface{}{
			"chain":     p.Chain,
			"asset":     p.Asset,
			"value_usd": p.ValueUSD,
			"apy":       p.APY,
		}
	}

	var lastRebalanceISO string
	if !c.lastRebalance.IsZero() {
		lastRebalanceISO = c.lastRebalance.UTC().Format(time.RFC3339)
	}

	return map[string]interface{}{
		"allocation":        alloc,
		"free_capital_usd":  c.freeCapital,
		"top_rates":         topPerChain,
		"last_rebalance_at": lastRebalanceISO,
		"realized_24h_usd":  c.realizedYield24hLocked(),
		"rebalances_24h":    c.rebalancesInLastLocked(24 * time.Hour),
	}, nil
}

// ApplyAdjustments clamps to the legal ranges in
// cross_chain_yield_config.go and ignores unknown keys. Returns an error
// only on type mismatch (the strategist sent a string where a number was
// expected); out-of-range values are clamped rather than rejected.
func (c *CrossChainYield) ApplyAdjustments(adjustments map[string]interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k, raw := range adjustments {
		switch k {
		case "min_yield_diff_bps":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("cross_chain_yield: min_yield_diff_bps not numeric: %T", raw)
			}
			c.cfg.MinYieldDiffBps = clampFloat(v, minYieldDiffBpsFloor, minYieldDiffBpsCeil)
		case "max_single_protocol_pct":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("cross_chain_yield: max_single_protocol_pct not numeric: %T", raw)
			}
			c.cfg.MaxSingleProtocolPct = clampFloat(v, maxSingleProtocolFloor, maxSingleProtocolCeil)
		case "bridge_cost_threshold":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("cross_chain_yield: bridge_cost_threshold not numeric: %T", raw)
			}
			c.cfg.BridgeCostThreshold = clampFloat(v, bridgeCostFloor, bridgeCostCeil)
		case "rebalance_interval_secs":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("cross_chain_yield: rebalance_interval_secs not numeric: %T", raw)
			}
			c.cfg.RebalanceIntervalSecs = clampInt(int(v), rebalanceIntervalFloor, rebalanceIntervalCeil)
		case "check_interval_secs":
			v, ok := toFloat(raw)
			if !ok {
				return fmt.Errorf("cross_chain_yield: check_interval_secs not numeric: %T", raw)
			}
			c.cfg.CheckIntervalSecs = clampFloat(v, checkIntervalSecsFloor, checkIntervalSecsCeil)
		default:
			// unknown keys are silently dropped — strategist may emit
			// fields that belong to a sibling task type.
		}
	}
	return nil
}

func (c *CrossChainYield) GetPositionValue(_ context.Context) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total float64
	for _, p := range c.allocation {
		total += p.ValueUSD
	}
	return total, nil
}

func (c *CrossChainYield) CloseAllPositions(_ context.Context) ([]Trade, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.allocation) == 0 {
		return nil, nil
	}
	now := c.deps.Now()
	trades := make([]Trade, 0, len(c.allocation))
	for proto, p := range c.allocation {
		trades = append(trades, Trade{
			Chain:        p.Chain,
			TradeType:    "yield_exit",
			TokenPair:    p.Asset,
			DEX:          proto,
			AmountIn:     p.ValueUSD,
			AmountOut:    p.ValueUSD,
			IsPaperTrade: true,
			ExecutedAt:   now,
			Metadata: map[string]interface{}{
				"protocol": proto,
				"apy":      p.APY,
				"reason":   "close_all",
			},
		})
		c.freeCapital += p.ValueUSD
	}
	c.allocation = map[string]positionState{}
	// Stable order — tests assert on emitted trades.
	sort.Slice(trades, func(i, j int) bool { return trades[i].DEX < trades[j].DEX })
	return trades, nil
}

// --- internal helpers ------------------------------------------------------

func (c *CrossChainYield) totalCapitalLocked() float64 {
	total := c.freeCapital
	for _, p := range c.allocation {
		total += p.ValueUSD
	}
	return total
}

func (c *CrossChainYield) currentBlendedAPY() float64 {
	var total, weighted float64
	for _, p := range c.allocation {
		total += p.ValueUSD
		weighted += p.ValueUSD * p.APY
	}
	if total <= 0 {
		return 0
	}
	return weighted / total
}

func (c *CrossChainYield) fetchAllowedRates(ctx context.Context) ([]chain.YieldRate, error) {
	allowed := make(map[string]struct{}, len(c.cfg.AllowedProtocols))
	for _, p := range c.cfg.AllowedProtocols {
		allowed[p] = struct{}{}
	}

	chainNames := make([]string, 0, len(c.deps.Clients))
	for name := range c.deps.Clients {
		chainNames = append(chainNames, name)
	}
	sort.Strings(chainNames)

	var all []chain.YieldRate
	for _, name := range chainNames {
		client := c.deps.Clients[name]
		rates, err := client.GetYieldRates(ctx, c.cfg.AllowedProtocols)
		if err != nil {
			return nil, fmt.Errorf("%s.GetYieldRates: %w", name, err)
		}
		for _, r := range rates {
			if _, ok := allowed[r.Protocol]; !ok {
				continue
			}
			if r.Chain == "" {
				r.Chain = name
			}
			all = append(all, r)
		}
	}
	return all, nil
}

// chooseTarget picks one or more protocols to allocate to such that no
// single protocol exceeds MaxSingleProtocolPct of total capital. The
// returned map is protocol -> intended fraction of capital. Sum is 1.0
// when there is enough headroom; otherwise the residual stays in
// freeCapital after rebalance.
func (c *CrossChainYield) chooseTarget(rates []chain.YieldRate) map[string]chain.YieldRate {
	if len(rates) == 0 {
		return nil
	}
	sorted := make([]chain.YieldRate, len(rates))
	copy(sorted, rates)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].APY > sorted[j].APY })

	cap := c.cfg.MaxSingleProtocolPct
	if cap <= 0 {
		cap = 1.0
	}
	needed := 1.0
	out := map[string]chain.YieldRate{}
	for _, r := range sorted {
		take := cap
		if take > needed {
			take = needed
		}
		// Tag the share onto the YieldRate via a copy-by-key into the
		// caller's map; we encode the share in the caller's allocation
		// step below by looking up totals.
		out[r.Protocol] = r
		needed -= take
		if needed <= 0 {
			break
		}
	}
	return out
}

// blendedAPY weighs each rate equally up to the per-protocol cap; the
// caller has already truncated the rate set in chooseTarget so this is a
// simple cap-weighted mean.
func blendedAPY(target map[string]chain.YieldRate) float64 {
	if len(target) == 0 {
		return 0
	}
	var sum, weight float64
	share := 1.0 / float64(len(target))
	for _, r := range target {
		sum += share * r.APY
		weight += share
	}
	if weight == 0 {
		return 0
	}
	return sum / weight
}

// estimateBridgeCost returns the modeled USD cost of a rebalance that
// crosses chains. Real bridging is out of scope for chunk 11 — the
// number here is a placeholder so BridgeCostThreshold can still gate
// the decision deterministically.
func (c *CrossChainYield) estimateBridgeCost(target map[string]chain.YieldRate) float64 {
	currentChains := map[string]bool{}
	for _, p := range c.allocation {
		currentChains[p.Chain] = true
	}
	if len(c.allocation) == 0 {
		// First entry — no bridge.
		return 0
	}
	for _, r := range target {
		if !currentChains[r.Chain] {
			// At least one leg crosses chains. Model: 0.5 % of capital
			// (matches a typical Wormhole / Across fee envelope on
			// stable balances). Halved for clarity vs a sharp ceiling.
			return c.totalCapitalLocked() * 0.005
		}
	}
	return 0
}

func (c *CrossChainYield) rebalance(now time.Time, total float64, target map[string]chain.YieldRate, bridgeCost, diffBps float64) []Trade {
	var trades []Trade

	// Close all existing positions (paper) — easier than partial
	// rebalances and matches the spec's "move capital" framing.
	for proto, p := range c.allocation {
		trades = append(trades, Trade{
			Chain:        p.Chain,
			TradeType:    "yield_exit",
			TokenPair:    p.Asset,
			DEX:          proto,
			AmountIn:     p.ValueUSD,
			AmountOut:    p.ValueUSD,
			IsPaperTrade: true,
			ExecutedAt:   now,
			Metadata: map[string]interface{}{
				"protocol": proto,
				"apy":      p.APY,
				"reason":   "rebalance",
			},
		})
	}
	c.freeCapital += sumValues(c.allocation)
	c.allocation = map[string]positionState{}

	// Pay the bridge cost, if any, from free capital.
	if bridgeCost > 0 {
		trades = append(trades, Trade{
			Chain:        c.cfg.PrimaryChain,
			TradeType:    "bridge",
			TokenPair:    "USD",
			DEX:          "mock_bridge",
			AmountIn:     bridgeCost,
			AmountOut:    0,
			FeePaid:      bridgeCost,
			IsPaperTrade: true,
			ExecutedAt:   now,
			Metadata: map[string]interface{}{
				"reason": "cross_chain_rebalance",
			},
		})
		c.freeCapital -= bridgeCost
		total -= bridgeCost
	}

	// Open each target position at its capped share. Iterate target in
	// sorted-protocol order so trade output is deterministic.
	protos := make([]string, 0, len(target))
	for p := range target {
		protos = append(protos, p)
	}
	sort.Strings(protos)

	cap := c.cfg.MaxSingleProtocolPct
	if cap <= 0 {
		cap = 1.0
	}
	deployed := 0.0
	for _, proto := range protos {
		r := target[proto]
		// equal split among the chosen set, bounded by the cap.
		share := 1.0 / float64(len(target))
		if share > cap {
			share = cap
		}
		amount := total * share
		if amount > total-deployed {
			amount = total - deployed
		}
		if amount <= 0 {
			continue
		}
		c.allocation[proto] = positionState{
			Chain:    r.Chain,
			Asset:    r.Asset,
			ValueUSD: amount,
			APY:      r.APY,
			OpenedAt: now,
		}
		c.freeCapital -= amount
		deployed += amount
		trades = append(trades, Trade{
			Chain:        r.Chain,
			TradeType:    "yield_enter",
			TokenPair:    r.Asset,
			DEX:          proto,
			AmountIn:     amount,
			AmountOut:    amount,
			IsPaperTrade: true,
			ExecutedAt:   now,
			Metadata: map[string]interface{}{
				"protocol": proto,
				"apy":      r.APY,
				"diff_bps": diffBps,
			},
		})
	}

	c.lastRebalance = now
	c.rebalanceHistory = append(c.rebalanceHistory, rebalanceEvent{
		At:         now,
		ToProto:    firstProto(protos),
		CapitalUSD: deployed,
		BridgeCost: bridgeCost,
		DiffBps:    diffBps,
	})
	return trades
}

func (c *CrossChainYield) realizedYield24hLocked() float64 {
	// Lightweight approximation — sum the APY × value × elapsed for
	// every currently-open position, capped at 24 h. Closed-position
	// realized yield would require a per-trade ledger we don't keep.
	now := c.deps.Now()
	cutoff := now.Add(-24 * time.Hour)
	var realized float64
	for _, p := range c.allocation {
		start := p.OpenedAt
		if start.Before(cutoff) {
			start = cutoff
		}
		elapsed := now.Sub(start)
		if elapsed <= 0 {
			continue
		}
		years := elapsed.Hours() / (24 * 365)
		realized += p.ValueUSD * p.APY * years
	}
	return realized
}

func (c *CrossChainYield) rebalancesInLastLocked(d time.Duration) int {
	cutoff := c.deps.Now().Add(-d)
	n := 0
	for _, ev := range c.rebalanceHistory {
		if ev.At.After(cutoff) {
			n++
		}
	}
	return n
}

func sumValues(m map[string]positionState) float64 {
	var total float64
	for _, p := range m {
		total += p.ValueUSD
	}
	return total
}

func firstProto(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// --- registration ----------------------------------------------------------

var crossChainYieldDeps atomic.Pointer[CrossChainYieldDeps]

// SetCrossChainYieldDeps wires the runtime's shared chain clients into
// the task factory. The runtime (chunk 14) calls this before booting
// any agent whose strategy_type is "cross_chain_yield". Tests construct
// the task directly via NewCrossChainYield and need not call this.
func SetCrossChainYieldDeps(d CrossChainYieldDeps) {
	cp := d
	crossChainYieldDeps.Store(&cp)
}

func crossChainYieldFactory(_ context.Context, raw json.RawMessage) (Task, error) {
	d := crossChainYieldDeps.Load()
	if d == nil {
		return nil, errors.New("cross_chain_yield: SetCrossChainYieldDeps not called")
	}
	var cfg CrossChainYieldConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("cross_chain_yield: decode config: %w", err)
		}
	}
	return NewCrossChainYield(*d, cfg)
}

func init() {
	Register("cross_chain_yield", crossChainYieldFactory)
}
