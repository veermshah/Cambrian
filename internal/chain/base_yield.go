package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// baseYieldClient gathers supply APYs from Aave and Morpho on Base.
// We deliberately reuse the GraphQL endpoints used elsewhere — the
// strategist's CrossChainYield task (chunk 11) needs APY+TVL per asset,
// not full reserve state.
//
// Aave V3 Subgraph: reserves[].liquidityRate is an APR in ray (1e27).
// At our holding cadences APR ≈ APY; chunk 11 may compound it later.
//
// Morpho Blue exposes `state.supplyApy` as a plain decimal already.

type baseYieldClient struct {
	http        *http.Client
	aaveURL     string
	morphoURL   string
}

func newBaseYieldClient(extra map[string]string) *baseYieldClient {
	c := &baseYieldClient{
		http:      &http.Client{Timeout: aggHTTPTimeout},
		aaveURL:   defaultAaveSubgraphURL,
		morphoURL: defaultMorphoGraphqlURL,
	}
	if v := extra["aave_subgraph_url"]; v != "" {
		c.aaveURL = v
	}
	if v := extra["morpho_url"]; v != "" {
		c.morphoURL = v
	}
	return c
}

const aaveReservesQuery = `
query Reserves {
  reserves(where: {isActive: true, isFrozen: false}) {
    symbol
    underlyingAsset
    liquidityRate
    totalLiquidity
    decimals
    price { priceInEth }
  }
}`

type aaveReservesResp struct {
	Data struct {
		Reserves []struct {
			Symbol          string `json:"symbol"`
			UnderlyingAsset string `json:"underlyingAsset"`
			LiquidityRate   string `json:"liquidityRate"`
			TotalLiquidity  string `json:"totalLiquidity"`
			Decimals        int    `json:"decimals"`
		} `json:"reserves"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const morphoMarketsQuery = `
query Markets($chainId: Int!) {
  markets(first: 100, where: {chainId_in: [$chainId]}) {
    items {
      uniqueKey
      loanAsset { address symbol decimals }
      state { supplyApy supplyAssetsUsd }
    }
  }
}`

type morphoMarketsResp struct {
	Data struct {
		Markets struct {
			Items []struct {
				UniqueKey string `json:"uniqueKey"`
				LoanAsset struct {
					Address  string `json:"address"`
					Symbol   string `json:"symbol"`
					Decimals int    `json:"decimals"`
				} `json:"loanAsset"`
				State struct {
					SupplyApy       float64 `json:"supplyApy"`
					SupplyAssetsUsd float64 `json:"supplyAssetsUsd"`
				} `json:"state"`
			} `json:"items"`
		} `json:"markets"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *baseYieldClient) fetchAaveRates(ctx context.Context) ([]YieldRate, error) {
	body, _ := json.Marshal(map[string]any{"query": aaveReservesQuery})
	respBody, err := c.gqlPost(ctx, c.aaveURL, body)
	if err != nil {
		return nil, fmt.Errorf("aave yield: %w", err)
	}
	var parsed aaveReservesResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("aave yield: decode: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("aave yield: graphql: %s", parsed.Errors[0].Message)
	}
	now := time.Now().UTC()
	out := make([]YieldRate, 0, len(parsed.Data.Reserves))
	for _, r := range parsed.Data.Reserves {
		rateRay, _ := strconv.ParseFloat(r.LiquidityRate, 64)
		apy := rateRay / 1e27 // ray → decimal APR
		liq, _ := strconv.ParseFloat(r.TotalLiquidity, 64)
		// totalLiquidity is in token atoms; we don't have a USD price here
		// without the oracle path, so report it in token units (chunk 11 can
		// re-price). Storing as TVL-in-token keeps the field non-zero.
		tvl := liq / pow10(r.Decimals)
		out = append(out, YieldRate{
			Chain:     "base",
			Protocol:  "aave",
			Asset:     r.UnderlyingAsset,
			APY:       apy,
			TVL:       tvl,
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *baseYieldClient) fetchMorphoRates(ctx context.Context, chainID uint64) ([]YieldRate, error) {
	body, _ := json.Marshal(map[string]any{
		"query":     morphoMarketsQuery,
		"variables": map[string]any{"chainId": chainID},
	})
	respBody, err := c.gqlPost(ctx, c.morphoURL, body)
	if err != nil {
		return nil, fmt.Errorf("morpho yield: %w", err)
	}
	var parsed morphoMarketsResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("morpho yield: decode: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("morpho yield: graphql: %s", parsed.Errors[0].Message)
	}
	now := time.Now().UTC()
	items := parsed.Data.Markets.Items
	// Morpho exposes one APY per market; multiple markets share the same
	// loan asset. Take the highest-APY market per asset for the strategist.
	best := map[string]YieldRate{}
	for _, m := range items {
		key := m.LoanAsset.Address
		cur, ok := best[key]
		if !ok || m.State.SupplyApy > cur.APY {
			best[key] = YieldRate{
				Chain:     "base",
				Protocol:  "morpho",
				Asset:     m.LoanAsset.Address,
				APY:       m.State.SupplyApy,
				TVL:       m.State.SupplyAssetsUsd,
				UpdatedAt: now,
			}
		}
	}
	out := make([]YieldRate, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	return out, nil
}

func (c *baseYieldClient) gqlPost(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
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

func pow10(n int) float64 {
	if n <= 0 {
		return 1
	}
	v := 1.0
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}
