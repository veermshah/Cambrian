package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MarginFi and Kamino each publish a JSON API with current pool / bank
// rates. We hit those APIs from GetYieldRates rather than scanning every
// reserve account on chain — the strategist only needs APY snapshots,
// updated every few minutes.
//
// URLs default to the public mainnet endpoints; tests and devnet
// integrations override via chain.Config.Extra: the keys
// "marginfi_yield_url" and "kamino_yield_url".

const (
	defaultMarginFiYieldURL = "https://api.marginfi.com/v2/banks"
	defaultKaminoYieldURL   = "https://api.kamino.finance/v2/markets"
	yieldHTTPTimeout        = 10 * time.Second
)

type yieldClient struct {
	http        *http.Client
	marginFiURL string
	kaminoURL   string
}

func newYieldClient(extra map[string]string) *yieldClient {
	mf := defaultMarginFiYieldURL
	if v, ok := extra["marginfi_yield_url"]; ok && v != "" {
		mf = v
	}
	kam := defaultKaminoYieldURL
	if v, ok := extra["kamino_yield_url"]; ok && v != "" {
		kam = v
	}
	return &yieldClient{
		http:        &http.Client{Timeout: yieldHTTPTimeout},
		marginFiURL: mf,
		kaminoURL:   kam,
	}
}

// fetch returns YieldRate rows for whichever subset of {marginfi, kamino}
// the caller requested. Unknown protocols are silently skipped: they
// land elsewhere in the swarm via the Base client (Aave, Morpho).
func (y *yieldClient) fetch(ctx context.Context, protocols []string) ([]YieldRate, error) {
	out := make([]YieldRate, 0)
	for _, p := range protocols {
		switch strings.ToLower(p) {
		case "marginfi":
			rates, err := y.fetchMarginFi(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, rates...)
		case "kamino":
			rates, err := y.fetchKamino(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, rates...)
		}
	}
	return out, nil
}

type marginFiBankResp struct {
	Banks []struct {
		Address     string  `json:"address"`
		MintSymbol  string  `json:"mintSymbol"`
		LendingAPY  float64 `json:"lendingApy"`
		BorrowAPY   float64 `json:"borrowApy"`
		TotalDeposits float64 `json:"totalDeposits"`
	} `json:"banks"`
}

func (y *yieldClient) fetchMarginFi(ctx context.Context) ([]YieldRate, error) {
	body, err := y.do(ctx, y.marginFiURL)
	if err != nil {
		return nil, fmt.Errorf("marginfi yield: %w", err)
	}
	var parsed marginFiBankResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("marginfi yield: decode: %w", err)
	}
	now := time.Now().UTC()
	out := make([]YieldRate, 0, len(parsed.Banks))
	for _, b := range parsed.Banks {
		if b.MintSymbol == "" {
			continue
		}
		out = append(out, YieldRate{
			Chain:     "solana",
			Protocol:  "marginfi",
			Asset:     b.MintSymbol,
			APY:       b.LendingAPY,
			TVL:       b.TotalDeposits,
			UpdatedAt: now,
		})
	}
	return out, nil
}

type kaminoMarketResp struct {
	Markets []struct {
		Reserves []struct {
			Symbol     string  `json:"symbol"`
			SupplyAPY  float64 `json:"supplyApy"`
			TotalSupply float64 `json:"totalSupplyUsd"`
		} `json:"reserves"`
	} `json:"markets"`
}

func (y *yieldClient) fetchKamino(ctx context.Context) ([]YieldRate, error) {
	body, err := y.do(ctx, y.kaminoURL)
	if err != nil {
		return nil, fmt.Errorf("kamino yield: %w", err)
	}
	var parsed kaminoMarketResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kamino yield: decode: %w", err)
	}
	now := time.Now().UTC()
	out := make([]YieldRate, 0)
	for _, m := range parsed.Markets {
		for _, r := range m.Reserves {
			if r.Symbol == "" {
				continue
			}
			out = append(out, YieldRate{
				Chain:     "solana",
				Protocol:  "kamino",
				Asset:     r.Symbol,
				APY:       r.SupplyAPY,
				TVL:       r.TotalSupply,
				UpdatedAt: now,
			})
		}
	}
	return out, nil
}

func (y *yieldClient) do(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := y.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, nil
}
