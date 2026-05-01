package chain

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAaveYieldRayToAPY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// liquidityRate 50000000000000000000000000 = 5e25 ray = 0.05 (5%) APR.
		_, _ = io.WriteString(w, `{
			"data": {
				"reserves": [
					{
						"symbol": "USDC",
						"underlyingAsset": "0xusdc",
						"liquidityRate": "50000000000000000000000000",
						"totalLiquidity": "5000000000",
						"decimals": 6
					},
					{
						"symbol": "WETH",
						"underlyingAsset": "0xweth",
						"liquidityRate": "12000000000000000000000000",
						"totalLiquidity": "100000000000000000000",
						"decimals": 18
					}
				]
			}
		}`)
	}))
	defer srv.Close()

	c := newBaseYieldClient(map[string]string{"aave_subgraph_url": srv.URL})
	got, err := c.fetchAaveRates(context.Background())
	if err != nil {
		t.Fatalf("fetchAaveRates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Protocol != "aave" || got[0].Chain != "base" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if math.Abs(got[0].APY-0.05) > 1e-9 {
		t.Errorf("USDC APY = %v, want 0.05", got[0].APY)
	}
	if got[0].TVL != 5000 {
		t.Errorf("USDC TVL = %v, want 5000 (5e9 atoms / 1e6)", got[0].TVL)
	}
	if math.Abs(got[1].APY-0.012) > 1e-9 {
		t.Errorf("WETH APY = %v, want 0.012", got[1].APY)
	}
	if got[1].TVL != 100 {
		t.Errorf("WETH TVL = %v, want 100", got[1].TVL)
	}
	if got[0].UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestMorphoYieldDedupesPerAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Two markets share USDC loan asset; client should keep the higher APY.
		_, _ = io.WriteString(w, `{
			"data": {
				"markets": {
					"items": [
						{
							"uniqueKey": "0xkey1",
							"loanAsset": {"address": "0xusdc", "symbol": "USDC", "decimals": 6},
							"state": {"supplyApy": 0.041, "supplyAssetsUsd": 100000}
						},
						{
							"uniqueKey": "0xkey2",
							"loanAsset": {"address": "0xusdc", "symbol": "USDC", "decimals": 6},
							"state": {"supplyApy": 0.067, "supplyAssetsUsd": 250000}
						},
						{
							"uniqueKey": "0xkey3",
							"loanAsset": {"address": "0xweth", "symbol": "WETH", "decimals": 18},
							"state": {"supplyApy": 0.015, "supplyAssetsUsd": 50000}
						}
					]
				}
			}
		}`)
	}))
	defer srv.Close()

	c := newBaseYieldClient(map[string]string{"morpho_url": srv.URL})
	got, err := c.fetchMorphoRates(context.Background(), 8453)
	if err != nil {
		t.Fatalf("fetchMorphoRates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (deduped)", len(got))
	}
	byAsset := map[string]YieldRate{}
	for _, r := range got {
		byAsset[r.Asset] = r
	}
	usdc, ok := byAsset["0xusdc"]
	if !ok {
		t.Fatal("missing 0xusdc rate")
	}
	if usdc.APY != 0.067 {
		t.Errorf("USDC APY = %v, want 0.067 (highest of two markets)", usdc.APY)
	}
	if usdc.TVL != 250000 {
		t.Errorf("USDC TVL = %v, want 250000", usdc.TVL)
	}
	if byAsset["0xweth"].APY != 0.015 {
		t.Errorf("WETH APY = %v", byAsset["0xweth"].APY)
	}
}

func TestAaveYieldGraphqlError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"reserves field deprecated"}]}`)
	}))
	defer srv.Close()
	c := newBaseYieldClient(map[string]string{"aave_subgraph_url": srv.URL})
	_, err := c.fetchAaveRates(context.Background())
	if err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("err = %v", err)
	}
}

func TestMorphoYieldHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `down`)
	}))
	defer srv.Close()
	c := newBaseYieldClient(map[string]string{"morpho_url": srv.URL})
	_, err := c.fetchMorphoRates(context.Background(), 8453)
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("err = %v", err)
	}
}
