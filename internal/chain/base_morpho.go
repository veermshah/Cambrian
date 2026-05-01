package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Morpho Blue exposes a public GraphQL endpoint at
// https://blue-api.morpho.org/graphql with `marketPositions` indexed
// per chain. Unlike Aave we get a precomputed `healthFactor` directly
// from the indexer — chunk 26 will still re-derive it on-chain before
// liquidating, but for discovery the indexer's number is fine.
//
// Morpho is one big shared indexer (no separate Subgraph URL per chain),
// so the chainID flows in as a GraphQL variable rather than into the URL.

const (
	defaultMorphoGraphqlURL = "https://blue-api.morpho.org/graphql"
	morphoDefaultLimit      = 200
	morphoHealthFactorMax   = 1.05 // only fetch positions worth a strategist look
)

type morphoClient struct {
	http       *http.Client
	graphqlURL string
}

func newMorphoClient(extra map[string]string) *morphoClient {
	url := defaultMorphoGraphqlURL
	if v := extra["morpho_url"]; v != "" {
		url = v
	}
	return &morphoClient{
		http:       &http.Client{Timeout: aggHTTPTimeout},
		graphqlURL: url,
	}
}

const morphoPositionsQuery = `
query Liquidatable($limit: Int!, $chainId: Int!, $hfMax: Float!) {
  marketPositions(
    first: $limit,
    where: {chainId_in: [$chainId], healthFactor_lte: $hfMax},
    orderBy: HealthFactor,
    orderDirection: Asc
  ) {
    items {
      user { address }
      market {
        collateralAsset { address }
        loanAsset { address }
        lltv
      }
      collateral
      borrowAssets
      healthFactor
    }
  }
}`

type morphoGraphqlResp struct {
	Data struct {
		MarketPositions struct {
			Items []struct {
				User struct {
					Address string `json:"address"`
				} `json:"user"`
				Market struct {
					CollateralAsset struct {
						Address string `json:"address"`
					} `json:"collateralAsset"`
					LoanAsset struct {
						Address string `json:"address"`
					} `json:"loanAsset"`
					LLTV string `json:"lltv"`
				} `json:"market"`
				Collateral   string  `json:"collateral"`
				BorrowAssets string  `json:"borrowAssets"`
				HealthFactor float64 `json:"healthFactor"`
			} `json:"items"`
		} `json:"marketPositions"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (m *morphoClient) fetchPositions(ctx context.Context, chainID uint64, limit int) ([]LendingPosition, error) {
	if limit <= 0 {
		limit = morphoDefaultLimit
	}
	body, err := json.Marshal(map[string]any{
		"query": morphoPositionsQuery,
		"variables": map[string]any{
			"limit":   limit,
			"chainId": chainID,
			"hfMax":   morphoHealthFactorMax,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("morpho: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("morpho: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("morpho: do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("morpho: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("morpho: status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	var parsed morphoGraphqlResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("morpho: decode: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("morpho: graphql error: %s", parsed.Errors[0].Message)
	}
	items := parsed.Data.MarketPositions.Items
	out := make([]LendingPosition, 0, len(items))
	for _, it := range items {
		coll, _ := strconv.ParseFloat(it.Collateral, 64)
		debt, _ := strconv.ParseFloat(it.BorrowAssets, 64)
		// Morpho LLTV is a 18-decimal scaled WAD (e.g. 0.86 -> 860000000000000000).
		// Convert to bonus-equivalent bps (10000 + (1-LLTV)*10000) so the
		// downstream strategist can compare against Aave's reserveLiquidationBonus.
		// LLTV decoding lives in chunk 26's oracle path; for now we emit raw
		// bps = 10000 - lltvBps so the field has *some* signal.
		lltvRaw, _ := strconv.ParseFloat(it.Market.LLTV, 64)
		lltv := lltvRaw / 1e18
		var bonusBps float64
		if lltv > 0 && lltv < 1 {
			bonusBps = (1 - lltv) * 10000
		}
		out = append(out, LendingPosition{
			Chain:            "base",
			Protocol:         "morpho",
			Owner:            it.User.Address,
			CollateralAsset:  it.Market.CollateralAsset.Address,
			CollateralAmt:    coll,
			DebtAsset:        it.Market.LoanAsset.Address,
			DebtAmt:          debt,
			HealthFactor:     it.HealthFactor,
			LiquidationBonus: bonusBps,
		})
	}
	return out, nil
}
