package chain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAaveFetchPositionsMapsRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("server: decode body: %v", err)
		}
		if !strings.Contains(req.Query, "userReserves") {
			t.Errorf("query missing userReserves: %s", req.Query)
		}
		if got := req.Variables["limit"]; got != float64(50) {
			t.Errorf("limit variable = %v, want 50", got)
		}
		_, _ = io.WriteString(w, `{
			"data": {
				"userReserves": [
					{
						"user": {"id": "0xowner1"},
						"reserve": {
							"symbol": "USDC",
							"underlyingAsset": "0xusdc",
							"reserveLiquidationBonus": "10500"
						},
						"currentATokenBalance": "2000000000",
						"currentTotalDebt": "1500000000"
					},
					{
						"user": {"id": "0xowner2"},
						"reserve": {
							"symbol": "WETH",
							"underlyingAsset": "0xweth",
							"reserveLiquidationBonus": "10750"
						},
						"currentATokenBalance": "500000000000000000",
						"currentTotalDebt": "300000000000000000"
					}
				]
			}
		}`)
	}))
	defer srv.Close()

	a := newAaveClient(map[string]string{"aave_subgraph_url": srv.URL})
	got, err := a.fetchPositions(context.Background(), 50)
	if err != nil {
		t.Fatalf("fetchPositions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	p1 := got[0]
	if p1.Chain != "base" || p1.Protocol != "aave" {
		t.Errorf("p1 chain/protocol = %q/%q", p1.Chain, p1.Protocol)
	}
	if p1.Owner != "0xowner1" {
		t.Errorf("p1.Owner = %q", p1.Owner)
	}
	if p1.CollateralAsset != "0xusdc" || p1.DebtAsset != "0xusdc" {
		t.Errorf("p1 assets = %q / %q", p1.CollateralAsset, p1.DebtAsset)
	}
	if p1.DebtAmt != 1.5e9 {
		t.Errorf("p1.DebtAmt = %v, want 1.5e9", p1.DebtAmt)
	}
	if p1.CollateralAmt != 2e9 {
		t.Errorf("p1.CollateralAmt = %v, want 2e9", p1.CollateralAmt)
	}
	if p1.LiquidationBonus != 10500 {
		t.Errorf("p1.LiquidationBonus = %v, want 10500", p1.LiquidationBonus)
	}
	if p1.HealthFactor != 0 {
		t.Errorf("p1.HealthFactor = %v, want 0 (deferred)", p1.HealthFactor)
	}
}

func TestAaveDefaultLimitWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		if got := req.Variables["limit"]; got != float64(aaveDefaultLimit) {
			t.Errorf("limit variable = %v, want default %d", got, aaveDefaultLimit)
		}
		_, _ = io.WriteString(w, `{"data":{"userReserves":[]}}`)
	}))
	defer srv.Close()

	a := newAaveClient(map[string]string{"aave_subgraph_url": srv.URL})
	got, err := a.fetchPositions(context.Background(), 0)
	if err != nil {
		t.Fatalf("fetchPositions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestAaveGraphqlErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"indexing_error: subgraph failed"}]}`)
	}))
	defer srv.Close()

	a := newAaveClient(map[string]string{"aave_subgraph_url": srv.URL})
	_, err := a.fetchPositions(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "indexing_error") {
		t.Fatalf("err = %v, want graphql error", err)
	}
}

func TestAaveHTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `gateway down`)
	}))
	defer srv.Close()

	a := newAaveClient(map[string]string{"aave_subgraph_url": srv.URL})
	_, err := a.fetchPositions(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("err = %v, want status 502", err)
	}
}

func TestAaveDefaultURLWhenExtraEmpty(t *testing.T) {
	a := newAaveClient(nil)
	if a.subgraphURL != defaultAaveSubgraphURL {
		t.Errorf("subgraphURL = %q, want default %q", a.subgraphURL, defaultAaveSubgraphURL)
	}
}
