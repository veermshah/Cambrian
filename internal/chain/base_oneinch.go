package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// 1inch Swap API v6 (https://portal.1inch.dev/documentation/apis/swap).
// The public endpoint requires an API key (free tier sufficient for our
// strategist polling cadence). Paraswap is a fallback when 1inch is
// rate-limiting or fails.
//
// 1inch does not deploy on testnets — only mainnet chain IDs. On
// Sepolia (chain id 84532) the aggregator returns "Chain id is not
// supported"; the BaseClient surfaces that error rather than masking
// it, since the swarm's strategist gates on it.
//
//   Base mainnet:    chainID 8453
//   Base Sepolia:    chainID 84532  (no 1inch support)
//   Ethereum mainnet:chainID 1
//   Ethereum Sepolia:chainID 11155111  (no 1inch support)

const (
	oneInchDefaultBaseURL  = "https://api.1inch.dev/swap/v6.0"
	paraswapDefaultBaseURL = "https://api.paraswap.io/swap"
	aggHTTPTimeout         = 8 * time.Second
)

type aggregatorClient struct {
	http       *http.Client
	oneInchURL string
	oneInchKey string
	paraswapURL string
}

func newAggregatorClient(extra map[string]string) *aggregatorClient {
	c := &aggregatorClient{
		http:        &http.Client{Timeout: aggHTTPTimeout},
		oneInchURL:  oneInchDefaultBaseURL,
		paraswapURL: paraswapDefaultBaseURL,
	}
	if v := extra["oneinch_url"]; v != "" {
		c.oneInchURL = v
	}
	if v := extra["oneinch_api_key"]; v != "" {
		c.oneInchKey = v
	}
	if v := extra["paraswap_url"]; v != "" {
		c.paraswapURL = v
	}
	return c
}

// oneInchQuoteResp covers the fields we read from /quote. RawResp is the
// full body, kept around so ExecuteSwap can fall straight into /swap
// without redoing the route discovery.
type oneInchQuoteResp struct {
	DstAmount string `json:"dstAmount"`
	Gas       int64  `json:"gas"`
}

func (a *aggregatorClient) oneInchGetQuote(ctx context.Context, chainID uint64, srcMint, dstMint string, amountAtomic string) (*oneInchQuoteResp, []byte, error) {
	q := url.Values{}
	q.Set("src", srcMint)
	q.Set("dst", dstMint)
	q.Set("amount", amountAtomic)
	q.Set("includeGas", "true")
	endpoint := fmt.Sprintf("%s/%d/quote?%s", a.oneInchURL, chainID, q.Encode())
	body, err := a.do(ctx, http.MethodGet, endpoint, nil, true)
	if err != nil {
		return nil, nil, fmt.Errorf("1inch quote: %w", err)
	}
	var parsed oneInchQuoteResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("1inch quote: decode: %w", err)
	}
	return &parsed, body, nil
}

// oneInchSwapResp is the /swap response — `tx` carries the to/data/value
// the BaseClient will sign and submit.
type oneInchSwapResp struct {
	DstAmount string `json:"dstAmount"`
	Tx        struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Data     string `json:"data"`
		Value    string `json:"value"`
		Gas      uint64 `json:"gas"`
		GasPrice string `json:"gasPrice"`
	} `json:"tx"`
}

func (a *aggregatorClient) oneInchBuildSwap(ctx context.Context, chainID uint64, srcMint, dstMint, amountAtomic, fromAddr string, slippagePct float64) (*oneInchSwapResp, error) {
	q := url.Values{}
	q.Set("src", srcMint)
	q.Set("dst", dstMint)
	q.Set("amount", amountAtomic)
	q.Set("from", fromAddr)
	q.Set("slippage", strconv.FormatFloat(slippagePct, 'f', -1, 64))
	q.Set("disableEstimate", "false")
	endpoint := fmt.Sprintf("%s/%d/swap?%s", a.oneInchURL, chainID, q.Encode())
	body, err := a.do(ctx, http.MethodGet, endpoint, nil, true)
	if err != nil {
		return nil, fmt.Errorf("1inch swap: %w", err)
	}
	var parsed oneInchSwapResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("1inch swap: decode: %w", err)
	}
	if parsed.Tx.To == "" || parsed.Tx.Data == "" {
		return nil, fmt.Errorf("1inch swap: empty tx in response")
	}
	return &parsed, nil
}

// paraswapQuoteResp keeps the bare minimum fields. Paraswap's prices
// API is free-tier; we use it as a fallback when 1inch errors.
type paraswapQuoteResp struct {
	PriceRoute struct {
		DestAmount string `json:"destAmount"`
		GasCost    string `json:"gasCost"`
	} `json:"priceRoute"`
}

func (a *aggregatorClient) paraswapGetQuote(ctx context.Context, chainID uint64, srcMint, dstMint, amountAtomic string) (*paraswapQuoteResp, []byte, error) {
	q := url.Values{}
	q.Set("srcToken", srcMint)
	q.Set("destToken", dstMint)
	q.Set("amount", amountAtomic)
	q.Set("network", strconv.FormatUint(chainID, 10))
	q.Set("side", "SELL")
	endpoint := fmt.Sprintf("%s/prices?%s", a.paraswapURL, q.Encode())
	body, err := a.do(ctx, http.MethodGet, endpoint, nil, false)
	if err != nil {
		return nil, nil, fmt.Errorf("paraswap quote: %w", err)
	}
	var parsed paraswapQuoteResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("paraswap quote: decode: %w", err)
	}
	if parsed.PriceRoute.DestAmount == "" {
		return nil, nil, fmt.Errorf("paraswap quote: empty priceRoute")
	}
	return &parsed, body, nil
}

func (a *aggregatorClient) do(ctx context.Context, method, endpoint string, body io.Reader, oneInch bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if oneInch && a.oneInchKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.oneInchKey)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	return respBody, nil
}

// _ = bytes.Reader keeps the import live for future POST bodies.
var _ = bytes.NewReader
