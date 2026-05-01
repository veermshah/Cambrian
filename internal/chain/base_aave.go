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

// Aave V3 publishes a public Subgraph (GraphQL) per chain. The strategist's
// liquidation-hunting task scans userReserves where currentTotalDebt > 0
// and builds LendingPosition rows from that. Direct contract calls
// (Pool.getUserAccountData) only work per known user — Subgraphs let us
// fan out across the whole market in a single query.
//
//   Base mainnet:  https://api.thegraph.com/subgraphs/name/aave/protocol-v3-base
//   Sepolia:       https://api.thegraph.com/subgraphs/name/aave/protocol-v3-sepolia
//
// URLs override via Config.Extra["aave_subgraph_url"]. Result limit is
// configurable so tests can set a small cap and not depend on Subgraph
// pagination defaults.

const (
	defaultAaveSubgraphURL = "https://api.thegraph.com/subgraphs/name/aave/protocol-v3-base"
	aaveDefaultLimit       = 200
)

type aaveClient struct {
	http        *http.Client
	subgraphURL string
}

func newAaveClient(extra map[string]string) *aaveClient {
	url := defaultAaveSubgraphURL
	if v := extra["aave_subgraph_url"]; v != "" {
		url = v
	}
	return &aaveClient{
		http:        &http.Client{Timeout: aggHTTPTimeout},
		subgraphURL: url,
	}
}

// aaveUserReservesQuery returns up to `limit` rows of (user, reserve,
// debt). The Subgraph's `userReserves` entity is the per-user, per-asset
// view we want. We filter on currentTotalDebt > 0 to skip inactive rows.
const aaveUserReservesQuery = `
query Liquidatable($limit: Int!) {
  userReserves(first: $limit, where: {currentTotalDebt_gt: 0}, orderBy: currentTotalDebt, orderDirection: desc) {
    user { id }
    reserve {
      symbol
      underlyingAsset
      reserveLiquidationBonus
    }
    currentATokenBalance
    currentTotalDebt
  }
}`

type aaveSubgraphResp struct {
	Data struct {
		UserReserves []struct {
			User    struct{ ID string `json:"id"` } `json:"user"`
			Reserve struct {
				Symbol                  string `json:"symbol"`
				UnderlyingAsset         string `json:"underlyingAsset"`
				ReserveLiquidationBonus string `json:"reserveLiquidationBonus"`
			} `json:"reserve"`
			CurrentATokenBalance string `json:"currentATokenBalance"`
			CurrentTotalDebt     string `json:"currentTotalDebt"`
		} `json:"userReserves"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (a *aaveClient) fetchPositions(ctx context.Context, limit int) ([]LendingPosition, error) {
	if limit <= 0 {
		limit = aaveDefaultLimit
	}
	body, err := json.Marshal(map[string]any{
		"query":     aaveUserReservesQuery,
		"variables": map[string]any{"limit": limit},
	})
	if err != nil {
		return nil, fmt.Errorf("aave: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.subgraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("aave: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aave: do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("aave: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("aave: status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	var parsed aaveSubgraphResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("aave: decode: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("aave: graphql error: %s", parsed.Errors[0].Message)
	}
	out := make([]LendingPosition, 0, len(parsed.Data.UserReserves))
	for _, r := range parsed.Data.UserReserves {
		debt, _ := strconv.ParseFloat(r.CurrentTotalDebt, 64)
		coll, _ := strconv.ParseFloat(r.CurrentATokenBalance, 64)
		bonusBps, _ := strconv.ParseFloat(r.Reserve.ReserveLiquidationBonus, 64)
		out = append(out, LendingPosition{
			Chain:           "base",
			Protocol:        "aave",
			Owner:           r.User.ID,
			CollateralAsset: r.Reserve.UnderlyingAsset,
			CollateralAmt:   coll,
			DebtAsset:       r.Reserve.UnderlyingAsset,
			DebtAmt:         debt,
			HealthFactor:    0, // computed by chunk 26 via Pool.getUserAccountData
			LiquidationBonus: bonusBps,
		})
	}
	return out, nil
}
