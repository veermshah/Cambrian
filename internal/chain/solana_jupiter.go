package chain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Jupiter v6 (https://station.jup.ag/docs/apis/swap-api) is the canonical
// Solana DEX aggregator. Two endpoints back our Quote / ExecuteSwap path:
//
//   GET  /quote      — find a route + price for tokenIn -> tokenOut
//   POST /swap       — turn that route into a base64 versioned tx, ready
//                      to sign and send.
//
// The default base URL points at the public quote API; tests override
// via WithBaseURL so they can route requests at an httptest.Server.

const (
	// Jupiter migrated the public quote/swap endpoints to lite-api in
	// late 2024; the v6 host (quote-api.jup.ag) still 200s but no longer
	// resolves from many networks. We default to the new URL and let
	// callers override via Config.Extra["jupiter_url"].
	jupiterDefaultBaseURL     = "https://lite-api.jup.ag/swap/v1"
	jupiterDefaultSlippageBps = 50 // 0.5%
	jupiterDefaultTimeout     = 8 * time.Second
)

type jupiterClient struct {
	base   string
	http   *http.Client
}

func newJupiterClient(baseURL string) *jupiterClient {
	if baseURL == "" {
		baseURL = jupiterDefaultBaseURL
	}
	return &jupiterClient{
		base: baseURL,
		http: &http.Client{Timeout: jupiterDefaultTimeout},
	}
}

// jupiterQuoteResp mirrors the v6 /quote response. Only the fields we
// consume are typed; the rest survive in routeRaw for the swap call.
type jupiterQuoteResp struct {
	InputMint            string `json:"inputMint"`
	OutputMint           string `json:"outputMint"`
	InAmount             string `json:"inAmount"`
	OutAmount            string `json:"outAmount"`
	OtherAmountThreshold string `json:"otherAmountThreshold"`
	SwapMode             string `json:"swapMode"`
	SlippageBps          int    `json:"slippageBps"`
	PriceImpactPct       string `json:"priceImpactPct"`
}

// getQuote calls Jupiter /quote. amountAtomic is the integer count of
// the input token's smallest unit (lamports for SOL, 6dp for USDC).
// SlippageBps defaults if zero.
func (j *jupiterClient) getQuote(ctx context.Context, inputMint, outputMint string, amountAtomic uint64, slippageBps int) (*jupiterQuoteResp, []byte, error) {
	if slippageBps == 0 {
		slippageBps = jupiterDefaultSlippageBps
	}
	q := url.Values{}
	q.Set("inputMint", inputMint)
	q.Set("outputMint", outputMint)
	q.Set("amount", strconv.FormatUint(amountAtomic, 10))
	q.Set("slippageBps", strconv.Itoa(slippageBps))
	q.Set("swapMode", "ExactIn")
	q.Set("onlyDirectRoutes", "false")

	endpoint := fmt.Sprintf("%s/quote?%s", j.base, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("jupiter quote: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := j.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("jupiter quote: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("jupiter quote: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("jupiter quote: status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	var parsed jupiterQuoteResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("jupiter quote: decode: %w", err)
	}
	return &parsed, body, nil
}

// jupiterSwapReq is the body of POST /swap. routeRaw is the full quote
// response we kept around in jupiterClient.getQuote.
type jupiterSwapReq struct {
	UserPublicKey         string          `json:"userPublicKey"`
	WrapAndUnwrapSol      bool            `json:"wrapAndUnwrapSol"`
	UseSharedAccounts     bool            `json:"useSharedAccounts"`
	DynamicComputeUnitLimit bool          `json:"dynamicComputeUnitLimit"`
	QuoteResponse         json.RawMessage `json:"quoteResponse"`
}

type jupiterSwapResp struct {
	SwapTransaction string `json:"swapTransaction"` // base64
}

// buildSwapTx asks Jupiter for the signed-ready, base64-encoded versioned
// transaction that executes routeRaw on behalf of userPubkey. Returns the
// raw transaction bytes (post base64 decode) — caller signs and sends.
func (j *jupiterClient) buildSwapTx(ctx context.Context, userPubkey string, routeRaw []byte) ([]byte, error) {
	body, err := json.Marshal(jupiterSwapReq{
		UserPublicKey:           userPubkey,
		WrapAndUnwrapSol:        true,
		UseSharedAccounts:       true,
		DynamicComputeUnitLimit: true,
		QuoteResponse:           routeRaw,
	})
	if err != nil {
		return nil, fmt.Errorf("jupiter swap: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.base+"/swap", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jupiter swap: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := j.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jupiter swap: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jupiter swap: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jupiter swap: status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	var parsed jupiterSwapResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("jupiter swap: decode: %w", err)
	}
	if parsed.SwapTransaction == "" {
		return nil, fmt.Errorf("jupiter swap: empty swapTransaction in response")
	}
	txBytes, err := base64.StdEncoding.DecodeString(parsed.SwapTransaction)
	if err != nil {
		return nil, fmt.Errorf("jupiter swap: b64 decode: %w", err)
	}
	return txBytes, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
