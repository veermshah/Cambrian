package chain

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// BaseClient implements ChainClient for Base — the L2 we target alongside
// Solana. It owns:
//
//   - an ethclient (Alchemy / Coinbase Base RPC; Config.RPCURL required),
//   - a 1inch + Paraswap aggregator client for swaps,
//   - Aave / Morpho Subgraph clients for lending discovery,
//   - a yield client for cross-chain APY signals.
//
// Mainnet is disabled until the launch chunk flips the gate; until then the
// factory only accepts "sepolia". 1inch does not deploy on testnets, so on
// Sepolia GetQuote returns the aggregator error verbatim — the strategist
// gates on it rather than masking it.

const (
	baseChainIDMainnet uint64 = 8453
	baseChainIDSepolia uint64 = 84532

	// Native ETH sentinel address used by 1inch + Paraswap.
	nativeETHAddress = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// erc20BalanceOfSelector = keccak256("balanceOf(address)")[:4]
var (
	erc20BalanceOfSelector = []byte{0x70, 0xa0, 0x82, 0x31}
	erc20DecimalsSelector  = []byte{0x31, 0x3c, 0xe5, 0x67}
)

type BaseClient struct {
	network    string
	chainID    uint64
	rpc        *ethclient.Client
	rpcURL     string
	aggregator *aggregatorClient
	aave       *aaveClient
	morpho     *morphoClient
	yield      *baseYieldClient
	masterKey  []byte
	decCache   map[string]uint8
}

var _ ChainClient = (*BaseClient)(nil)

func init() {
	Register("base", baseFactory)
}

func baseFactory(cfg Config) (ChainClient, error) {
	if cfg.Network == "mainnet" {
		return nil, errors.New("base: mainnet disabled")
	}
	if cfg.Network == "" {
		cfg.Network = "sepolia"
	}
	if cfg.Network != "sepolia" {
		return nil, fmt.Errorf("base: unsupported network %q (only sepolia until launch)", cfg.Network)
	}
	if cfg.RPCURL == "" {
		return nil, errors.New("base: RPCURL required")
	}
	cli, err := ethclient.DialContext(context.Background(), cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("base: dial rpc: %w", err)
	}
	c := &BaseClient{
		network:    cfg.Network,
		chainID:    baseChainIDSepolia,
		rpc:        cli,
		rpcURL:     cfg.RPCURL,
		aggregator: newAggregatorClient(cfg.Extra),
		aave:       newAaveClient(cfg.Extra),
		morpho:     newMorphoClient(cfg.Extra),
		yield:      newBaseYieldClient(cfg.Extra),
		decCache:   map[string]uint8{},
	}
	if mk := cfg.Extra["master_key_hex"]; mk != "" {
		raw, err := hex.DecodeString(mk)
		if err != nil {
			return nil, fmt.Errorf("base: master_key_hex: %w", err)
		}
		if len(raw) != masterKeySize {
			return nil, fmt.Errorf("base: master key must be %d bytes, got %d", masterKeySize, len(raw))
		}
		c.masterKey = raw
	}
	// Pre-seed common ERC-20 decimals so the swap path doesn't pay a
	// round-trip per quote. Sepolia USDC is the canonical Circle test deployment.
	c.decCache[strings.ToLower(nativeETHAddress)] = 18
	c.decCache["0x036cbd53842c5426634e7929541ec2318f3dcf7e"] = 6  // Sepolia USDC
	c.decCache["0x4200000000000000000000000000000000000006"] = 18 // Base WETH (predeploy)
	return c, nil
}

func (b *BaseClient) ChainName() string   { return "base" }
func (b *BaseClient) NativeToken() string { return "ETH" }

func (b *BaseClient) GetBalance(ctx context.Context, address string) (float64, error) {
	if !common.IsHexAddress(address) {
		return 0, fmt.Errorf("base: invalid address %q", address)
	}
	wei, err := b.rpc.BalanceAt(ctx, common.HexToAddress(address), nil)
	if err != nil {
		return 0, fmt.Errorf("base: balanceAt: %w", err)
	}
	return weiToEther(wei), nil
}

func (b *BaseClient) GetTokenBalance(ctx context.Context, address, tokenAddr string) (float64, error) {
	if !common.IsHexAddress(address) {
		return 0, fmt.Errorf("base: invalid owner %q", address)
	}
	if strings.EqualFold(tokenAddr, nativeETHAddress) {
		return b.GetBalance(ctx, address)
	}
	if !common.IsHexAddress(tokenAddr) {
		return 0, fmt.Errorf("base: invalid token %q", tokenAddr)
	}
	owner := common.HexToAddress(address)
	token := common.HexToAddress(tokenAddr)
	// balanceOf(address) — selector || left-padded owner address (32 bytes).
	calldata := append([]byte(nil), erc20BalanceOfSelector...)
	calldata = append(calldata, common.LeftPadBytes(owner.Bytes(), 32)...)
	raw, err := b.rpc.CallContract(ctx, ethereum.CallMsg{To: &token, Data: calldata}, nil)
	if err != nil {
		return 0, fmt.Errorf("base: balanceOf: %w", err)
	}
	if len(raw) == 0 {
		return 0, nil
	}
	bal := new(big.Int).SetBytes(raw)
	dec, err := b.decimalsFor(ctx, tokenAddr)
	if err != nil {
		return 0, err
	}
	return atomicToFloat(bal, dec), nil
}

func (b *BaseClient) GetQuote(ctx context.Context, tokenIn, tokenOut string, amount float64) (*Quote, error) {
	decIn, err := b.decimalsFor(ctx, tokenIn)
	if err != nil {
		return nil, err
	}
	decOut, err := b.decimalsFor(ctx, tokenOut)
	if err != nil {
		return nil, err
	}
	atomic := floatToAtomic(amount, decIn)
	parsed, raw, err := b.aggregator.oneInchGetQuote(ctx, b.chainID, tokenIn, tokenOut, atomic.String())
	if err != nil {
		return nil, err
	}
	dst, ok := new(big.Int).SetString(parsed.DstAmount, 10)
	if !ok {
		return nil, fmt.Errorf("base: quote: bad dstAmount %q", parsed.DstAmount)
	}
	out := atomicToFloat(dst, decOut)
	q := &Quote{
		Chain:       "base",
		DEX:         "1inch",
		TokenIn:     tokenIn,
		TokenOut:    tokenOut,
		AmountIn:    amount,
		AmountOut:   out,
		GeneratedAt: time.Now().UTC(),
		RouteRaw:    raw,
	}
	if amount > 0 {
		q.Price = out / amount
	}
	return q, nil
}

func (b *BaseClient) ExecuteSwap(ctx context.Context, quote *Quote, wallet *Wallet) (*TxResult, error) {
	if quote == nil {
		return nil, errors.New("base: ExecuteSwap nil quote")
	}
	if wallet == nil {
		return nil, errors.New("base: ExecuteSwap nil wallet")
	}
	priv, err := UnsealBaseWallet(b.masterKey, wallet)
	if err != nil {
		return nil, fmt.Errorf("base: unseal: %w", err)
	}
	defer zeroPriv(priv)

	decIn, err := b.decimalsFor(ctx, quote.TokenIn)
	if err != nil {
		return nil, err
	}
	atomic := floatToAtomic(quote.AmountIn, decIn)
	slip := float64(quote.SlippageBps) / 100.0 // bps → percent
	if slip <= 0 {
		slip = 0.5 // sane default for 1inch
	}
	swap, err := b.aggregator.oneInchBuildSwap(ctx, b.chainID, quote.TokenIn, quote.TokenOut, atomic.String(), wallet.Address, slip)
	if err != nil {
		return nil, err
	}
	tx, err := b.assembleSignedTx(ctx, priv, wallet.Address, swap)
	if err != nil {
		return nil, err
	}
	if IsDryRun(ctx) {
		return &TxResult{Signature: "dry-run", Success: true}, nil
	}
	if err := b.rpc.SendTransaction(ctx, tx); err != nil {
		return nil, fmt.Errorf("base: sendTransaction: %w", err)
	}
	return &TxResult{Signature: tx.Hash().Hex(), Success: true, GasUsed: tx.Gas()}, nil
}

func (b *BaseClient) assembleSignedTx(ctx context.Context, priv *ecdsa.PrivateKey, fromAddr string, swap *oneInchSwapResp) (*types.Transaction, error) {
	if !common.IsHexAddress(swap.Tx.To) {
		return nil, fmt.Errorf("base: aggregator returned bad `to` %q", swap.Tx.To)
	}
	to := common.HexToAddress(swap.Tx.To)
	data, err := hexutil.Decode(swap.Tx.Data)
	if err != nil {
		return nil, fmt.Errorf("base: decode tx data: %w", err)
	}
	value := big.NewInt(0)
	if swap.Tx.Value != "" && swap.Tx.Value != "0" {
		v, ok := new(big.Int).SetString(strings.TrimPrefix(swap.Tx.Value, "0x"), 0)
		if !ok {
			return nil, fmt.Errorf("base: bad tx.value %q", swap.Tx.Value)
		}
		value = v
	}
	nonce, err := b.rpc.PendingNonceAt(ctx, common.HexToAddress(fromAddr))
	if err != nil {
		return nil, fmt.Errorf("base: nonce: %w", err)
	}
	tip, err := b.rpc.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("base: gas tip: %w", err)
	}
	head, err := b.rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("base: head: %w", err)
	}
	// EIP-1559 fee cap: 2 * baseFee + tip, the convention go-ethereum's
	// SuggestGasPrice uses internally. Base supports EIP-1559 across both
	// mainnet and Sepolia.
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	gas := swap.Tx.Gas
	if gas == 0 {
		gas = 250000
	}
	raw := types.NewTx(&types.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(b.chainID),
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gas,
		To:        &to,
		Value:     value,
		Data:      data,
	})
	signed, err := types.SignTx(raw, types.NewLondonSigner(new(big.Int).SetUint64(b.chainID)), priv)
	if err != nil {
		return nil, fmt.Errorf("base: sign: %w", err)
	}
	return signed, nil
}

func (b *BaseClient) SimulateTransaction(ctx context.Context, txWrap *Transaction) (*SimResult, error) {
	if txWrap == nil || len(txWrap.Raw) == 0 {
		return nil, errors.New("base: SimulateTransaction requires Transaction.Raw")
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(txWrap.Raw); err != nil {
		return nil, fmt.Errorf("base: parse tx: %w", err)
	}
	from, err := types.Sender(types.NewLondonSigner(new(big.Int).SetUint64(b.chainID)), tx)
	if err != nil {
		return nil, fmt.Errorf("base: sender: %w", err)
	}
	msg := ethereum.CallMsg{
		From:     from,
		To:       tx.To(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Value:    tx.Value(),
		Data:     tx.Data(),
	}
	gas, err := b.rpc.EstimateGas(ctx, msg)
	if err != nil {
		return &SimResult{WouldSucceed: false, ErrorMsg: err.Error()}, nil
	}
	return &SimResult{WouldSucceed: true, GasEstimate: gas}, nil
}

func (b *BaseClient) SendTransaction(ctx context.Context, txWrap *Transaction, wallet *Wallet) (*TxResult, error) {
	if txWrap == nil || len(txWrap.Raw) == 0 {
		return nil, errors.New("base: SendTransaction requires Transaction.Raw")
	}
	if wallet == nil {
		return nil, errors.New("base: SendTransaction nil wallet")
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(txWrap.Raw); err != nil {
		return nil, fmt.Errorf("base: parse tx: %w", err)
	}
	// If the caller already signed the tx (V/R/S non-zero) we ship as-is.
	v, r, s := tx.RawSignatureValues()
	if v.Sign() != 0 || r.Sign() != 0 || s.Sign() != 0 {
		if IsDryRun(ctx) {
			return &TxResult{Signature: "dry-run", Success: true}, nil
		}
		if err := b.rpc.SendTransaction(ctx, tx); err != nil {
			return nil, fmt.Errorf("base: send pre-signed: %w", err)
		}
		return &TxResult{Signature: tx.Hash().Hex(), Success: true, GasUsed: tx.Gas()}, nil
	}
	// Otherwise sign with the wallet's key. This path is what arbitrary
	// (non-swap) actions take: the caller assembles the raw tx envelope,
	// we sign + broadcast.
	priv, err := UnsealBaseWallet(b.masterKey, wallet)
	if err != nil {
		return nil, fmt.Errorf("base: unseal: %w", err)
	}
	defer zeroPriv(priv)
	signed, err := types.SignTx(tx, types.NewLondonSigner(new(big.Int).SetUint64(b.chainID)), priv)
	if err != nil {
		return nil, fmt.Errorf("base: sign: %w", err)
	}
	if IsDryRun(ctx) {
		return &TxResult{Signature: "dry-run", Success: true}, nil
	}
	if err := b.rpc.SendTransaction(ctx, signed); err != nil {
		return nil, fmt.Errorf("base: send: %w", err)
	}
	return &TxResult{Signature: signed.Hash().Hex(), Success: true, GasUsed: signed.Gas()}, nil
}

func (b *BaseClient) GetLendingPositions(ctx context.Context, protocol string) ([]LendingPosition, error) {
	switch strings.ToLower(protocol) {
	case "aave":
		return b.aave.fetchPositions(ctx, 0)
	case "morpho":
		return b.morpho.fetchPositions(ctx, b.chainID, 0)
	default:
		return nil, fmt.Errorf("base: unknown lending protocol %q", protocol)
	}
}

func (b *BaseClient) GetYieldRates(ctx context.Context, protocols []string) ([]YieldRate, error) {
	out := []YieldRate{}
	for _, p := range protocols {
		switch strings.ToLower(p) {
		case "aave":
			rates, err := b.yield.fetchAaveRates(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, rates...)
		case "morpho":
			rates, err := b.yield.fetchMorphoRates(ctx, b.chainID)
			if err != nil {
				return nil, err
			}
			out = append(out, rates...)
		default:
			return nil, fmt.Errorf("base: unknown yield protocol %q", p)
		}
	}
	return out, nil
}

func (b *BaseClient) ExecuteLiquidation(_ context.Context, _ *LendingPosition, _ *Wallet) (*TxResult, error) {
	// Per spec: liquidation execution lands in chunk 26 (LiquidationHunting),
	// which builds the Pool.liquidationCall calldata against an oracle-derived
	// healthFactor. Until then we surface a clear unimplemented signal so
	// the strategist can route the task to the fake client during paper trades.
	return nil, errors.New("base: ExecuteLiquidation not implemented (chunk 26)")
}

func (b *BaseClient) decimalsFor(ctx context.Context, token string) (uint8, error) {
	key := strings.ToLower(token)
	if d, ok := b.decCache[key]; ok {
		return d, nil
	}
	if !common.IsHexAddress(token) {
		return 0, fmt.Errorf("base: invalid token %q", token)
	}
	addr := common.HexToAddress(token)
	raw, err := b.rpc.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: erc20DecimalsSelector}, nil)
	if err != nil {
		return 0, fmt.Errorf("base: decimals(): %w", err)
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("base: empty decimals() return for %q", token)
	}
	d := new(big.Int).SetBytes(raw)
	if !d.IsUint64() || d.Uint64() > 36 {
		return 0, fmt.Errorf("base: implausible decimals %s for %q", d.String(), token)
	}
	out := uint8(d.Uint64())
	b.decCache[key] = out
	return out, nil
}

func weiToEther(wei *big.Int) float64 {
	if wei == nil {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Float64()
	return f
}

func atomicToFloat(atomic *big.Int, decimals uint8) float64 {
	if atomic == nil {
		return 0
	}
	div := new(big.Float).SetInt(bigPow10(int(decimals)))
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(atomic), div).Float64()
	return f
}

func floatToAtomic(amount float64, decimals uint8) *big.Int {
	scaled := new(big.Float).Mul(big.NewFloat(amount), new(big.Float).SetInt(bigPow10(int(decimals))))
	out, _ := scaled.Int(nil)
	return out
}

func bigPow10(n int) *big.Int {
	out := big.NewInt(1)
	ten := big.NewInt(10)
	for i := 0; i < n; i++ {
		out.Mul(out, ten)
	}
	return out
}

func zeroPriv(priv *ecdsa.PrivateKey) {
	if priv == nil || priv.D == nil {
		return
	}
	priv.D.SetInt64(0)
}

