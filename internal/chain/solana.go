package chain

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// SolanaClient implements ChainClient for Solana. It owns:
//
//   - an RPC client (Helius devnet by default; Config.RPCURL overrides),
//   - a Jupiter v6 client for quotes + swap-tx assembly,
//   - a yield client for MarginFi / Kamino HTTP rate APIs.
//
// The factory is registered in init(), so importing internal/chain wires
// "solana" into the global registry. Mainnet network values are rejected
// until the launch chunk flips the gate.
type SolanaClient struct {
	network    string
	rpc        *rpc.Client
	jupiter    *jupiterClient
	yield      *yieldClient
	masterKey  []byte // for unsealing wallets at swap time
	mintCache  map[string]uint8
}

var _ ChainClient = (*SolanaClient)(nil)

func init() {
	Register("solana", solanaFactory)
}

func solanaFactory(cfg Config) (ChainClient, error) {
	if cfg.Network == "mainnet" {
		return nil, errors.New("solana: mainnet disabled")
	}
	if cfg.Network == "" {
		cfg.Network = "devnet"
	}
	rpcURL := cfg.RPCURL
	if rpcURL == "" {
		rpcURL = rpc.DevNet_RPC
	}
	c := &SolanaClient{
		network:   cfg.Network,
		rpc:       rpc.New(rpcURL),
		jupiter:   newJupiterClient(cfg.Extra["jupiter_url"]),
		yield:     newYieldClient(cfg.Extra),
		mintCache: map[string]uint8{},
	}
	if mk := cfg.Extra["master_key_hex"]; mk != "" {
		raw, err := hex.DecodeString(mk)
		if err != nil {
			return nil, fmt.Errorf("solana: master_key_hex: %w", err)
		}
		if len(raw) != masterKeySize {
			return nil, fmt.Errorf("solana: master key must be %d bytes, got %d", masterKeySize, len(raw))
		}
		c.masterKey = raw
	}
	// Pre-seed common mints so we don't pay the RPC round-trip on the
	// hot path. SOL is the wrapped-native mint Jupiter expects.
	c.mintCache[wrappedSOLMint] = 9
	c.mintCache["EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"] = 6 // mainnet USDC
	c.mintCache["Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"] = 6 // mainnet USDT
	return c, nil
}

const wrappedSOLMint = "So11111111111111111111111111111111111111112"

func (s *SolanaClient) ChainName() string   { return "solana" }
func (s *SolanaClient) NativeToken() string { return "SOL" }

func (s *SolanaClient) GetBalance(ctx context.Context, address string) (float64, error) {
	pk, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return 0, fmt.Errorf("solana: parse address: %w", err)
	}
	res, err := s.rpc.GetBalance(ctx, pk, rpc.CommitmentConfirmed)
	if err != nil {
		return 0, fmt.Errorf("solana: getBalance: %w", err)
	}
	return float64(res.Value) / 1e9, nil
}

func (s *SolanaClient) GetTokenBalance(ctx context.Context, address, tokenAddr string) (float64, error) {
	owner, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return 0, fmt.Errorf("solana: parse owner: %w", err)
	}
	mint, err := solana.PublicKeyFromBase58(tokenAddr)
	if err != nil {
		return 0, fmt.Errorf("solana: parse mint: %w", err)
	}
	out, err := s.rpc.GetTokenAccountsByOwner(ctx, owner,
		&rpc.GetTokenAccountsConfig{Mint: &mint},
		&rpc.GetTokenAccountsOpts{Encoding: solana.EncodingJSONParsed},
	)
	if err != nil {
		return 0, fmt.Errorf("solana: getTokenAccountsByOwner: %w", err)
	}
	if out == nil || len(out.Value) == 0 {
		return 0, nil
	}
	// Sum across token accounts owned by `owner` for this mint.
	var total float64
	for _, acct := range out.Value {
		bal, err := s.rpc.GetTokenAccountBalance(ctx, acct.Pubkey, rpc.CommitmentConfirmed)
		if err != nil {
			return 0, fmt.Errorf("solana: getTokenAccountBalance: %w", err)
		}
		amt, err := strconv.ParseFloat(bal.Value.Amount, 64)
		if err != nil {
			return 0, fmt.Errorf("solana: parse amount: %w", err)
		}
		total += amt / math.Pow10(int(bal.Value.Decimals))
		s.mintCache[tokenAddr] = bal.Value.Decimals
	}
	return total, nil
}

func (s *SolanaClient) GetQuote(ctx context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error) {
	decIn, err := s.decimalsFor(ctx, tokenIn)
	if err != nil {
		return nil, err
	}
	decOut, err := s.decimalsFor(ctx, tokenOut)
	if err != nil {
		return nil, err
	}
	atomicIn := uint64(math.Round(amount * math.Pow10(int(decIn))))
	parsed, raw, err := s.jupiter.getQuote(ctx, tokenIn, tokenOut, atomicIn, 0)
	if err != nil {
		return nil, err
	}
	outAtomic, err := strconv.ParseUint(parsed.OutAmount, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("solana: parse outAmount: %w", err)
	}
	priceImpact, _ := strconv.ParseFloat(parsed.PriceImpactPct, 64)
	outAmount := float64(outAtomic) / math.Pow10(int(decOut))
	q := &Quote{
		Chain:       "solana",
		DEX:         "jupiter",
		TokenIn:     tokenIn,
		TokenOut:    tokenOut,
		AmountIn:    amount,
		AmountOut:   outAmount,
		Price:       0,
		PriceImpact: priceImpact,
		SlippageBps: parsed.SlippageBps,
		GeneratedAt: time.Now().UTC(),
		RouteRaw:    raw,
	}
	if amount > 0 {
		q.Price = outAmount / amount
	}
	return q, nil
}

func (s *SolanaClient) ExecuteSwap(ctx context.Context, quote *Quote, wallet *Wallet) (*TxResult, error) {
	if quote == nil || quote.RouteRaw == nil {
		return nil, errors.New("solana: ExecuteSwap requires Quote with RouteRaw")
	}
	if wallet == nil {
		return nil, errors.New("solana: ExecuteSwap nil wallet")
	}
	priv, err := UnsealSolanaWallet(s.masterKey, wallet)
	if err != nil {
		return nil, fmt.Errorf("solana: unseal: %w", err)
	}
	txBytes, err := s.jupiter.buildSwapTx(ctx, wallet.Address, quote.RouteRaw)
	if err != nil {
		return nil, err
	}
	tx, err := solana.TransactionFromBytes(txBytes)
	if err != nil {
		return nil, fmt.Errorf("solana: parse swap tx: %w", err)
	}
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(priv.PublicKey()) {
			return &priv
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("solana: sign swap: %w", err)
	}
	if IsDryRun(ctx) {
		sim, err := s.rpc.SimulateTransaction(ctx, tx)
		if err != nil {
			return nil, fmt.Errorf("solana: simulate swap: %w", err)
		}
		return &TxResult{
			Success: sim != nil && sim.Value != nil && sim.Value.Err == nil,
			Signature: "dry-run",
		}, nil
	}
	sig, err := s.rpc.SendTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("solana: sendTransaction: %w", err)
	}
	return &TxResult{Signature: sig.String(), Success: true}, nil
}

func (s *SolanaClient) SimulateTransaction(ctx context.Context, txWrap *Transaction) (*SimResult, error) {
	if txWrap == nil || len(txWrap.Raw) == 0 {
		return nil, errors.New("solana: SimulateTransaction requires Transaction.Raw")
	}
	tx, err := solana.TransactionFromBytes(txWrap.Raw)
	if err != nil {
		return nil, fmt.Errorf("solana: parse tx: %w", err)
	}
	sim, err := s.rpc.SimulateTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("solana: simulate: %w", err)
	}
	if sim == nil || sim.Value == nil {
		return &SimResult{WouldSucceed: false, ErrorMsg: "empty sim response"}, nil
	}
	out := &SimResult{
		WouldSucceed: sim.Value.Err == nil,
		GasEstimate:  uint64(0), // CU consumption isn't gas; left zero for cross-chain consistency.
	}
	if sim.Value.Err != nil {
		out.ErrorMsg = fmt.Sprintf("%v", sim.Value.Err)
	}
	return out, nil
}

func (s *SolanaClient) SendTransaction(ctx context.Context, txWrap *Transaction, wallet *Wallet) (*TxResult, error) {
	if txWrap == nil || len(txWrap.Raw) == 0 {
		return nil, errors.New("solana: SendTransaction requires Transaction.Raw")
	}
	if wallet == nil {
		return nil, errors.New("solana: SendTransaction nil wallet")
	}
	priv, err := UnsealSolanaWallet(s.masterKey, wallet)
	if err != nil {
		return nil, fmt.Errorf("solana: unseal: %w", err)
	}
	tx, err := solana.TransactionFromBytes(txWrap.Raw)
	if err != nil {
		return nil, fmt.Errorf("solana: parse tx: %w", err)
	}
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(priv.PublicKey()) {
			return &priv
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("solana: sign: %w", err)
	}
	if IsDryRun(ctx) {
		return &TxResult{Signature: "dry-run", Success: true}, nil
	}
	sig, err := s.rpc.SendTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("solana: send: %w", err)
	}
	return &TxResult{Signature: sig.String(), Success: true}, nil
}

func (s *SolanaClient) GetLendingPositions(ctx context.Context, protocol string) ([]LendingPosition, error) {
	switch strings.ToLower(protocol) {
	case "marginfi":
		return fetchMarginFiPositions(ctx, s.rpc)
	case "kamino":
		return fetchKaminoPositions(ctx, s.rpc)
	default:
		return nil, fmt.Errorf("solana: unknown lending protocol %q", protocol)
	}
}

func (s *SolanaClient) GetYieldRates(ctx context.Context, protocols []string) ([]YieldRate, error) {
	return s.yield.fetch(ctx, protocols)
}

func (s *SolanaClient) ExecuteLiquidation(_ context.Context, _ *LendingPosition, _ *Wallet) (*TxResult, error) {
	// Per spec line 279: devnet liquidations are exercised in chunk 26
	// against a mock chain. The real implementation lands when mainnet
	// flips on.
	return nil, errors.New("solana: ExecuteLiquidation not implemented for devnet")
}

// decimalsFor returns the SPL token's decimal exponent, consulting the
// in-memory cache first and falling back to GetTokenSupply RPC.
func (s *SolanaClient) decimalsFor(ctx context.Context, mint string) (uint8, error) {
	if d, ok := s.mintCache[mint]; ok {
		return d, nil
	}
	pk, err := solana.PublicKeyFromBase58(mint)
	if err != nil {
		return 0, fmt.Errorf("solana: parse mint %q: %w", mint, err)
	}
	out, err := s.rpc.GetTokenSupply(ctx, pk, rpc.CommitmentConfirmed)
	if err != nil {
		return 0, fmt.Errorf("solana: getTokenSupply: %w", err)
	}
	if out == nil || out.Value == nil {
		return 0, fmt.Errorf("solana: empty token supply response for %q", mint)
	}
	s.mintCache[mint] = out.Value.Decimals
	return out.Value.Decimals, nil
}
