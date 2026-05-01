package chain

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMorphoFetchPositionsMapsItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !strings.Contains(req.Query, "marketPositions") {
			t.Errorf("query missing marketPositions: %s", req.Query)
		}
		if got := req.Variables["chainId"]; got != float64(8453) {
			t.Errorf("chainId = %v, want 8453", got)
		}
		if got := req.Variables["limit"]; got != float64(25) {
			t.Errorf("limit = %v, want 25", got)
		}
		_, _ = io.WriteString(w, `{
			"data": {
				"marketPositions": {
					"items": [
						{
							"user": {"address": "0xa1"},
							"market": {
								"collateralAsset": {"address": "0xweth"},
								"loanAsset": {"address": "0xusdc"},
								"lltv": "860000000000000000"
							},
							"collateral": "1000000000000000000",
							"borrowAssets": "2000000000",
							"healthFactor": 1.02
						},
						{
							"user": {"address": "0xa2"},
							"market": {
								"collateralAsset": {"address": "0xcbeth"},
								"loanAsset": {"address": "0xusdc"},
								"lltv": "910000000000000000"
							},
							"collateral": "500000000000000000",
							"borrowAssets": "950000000",
							"healthFactor": 1.04
						}
					]
				}
			}
		}`)
	}))
	defer srv.Close()

	m := newMorphoClient(map[string]string{"morpho_url": srv.URL})
	got, err := m.fetchPositions(context.Background(), 8453, 25)
	if err != nil {
		t.Fatalf("fetchPositions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	p := got[0]
	if p.Chain != "base" || p.Protocol != "morpho" {
		t.Errorf("chain/protocol = %q/%q", p.Chain, p.Protocol)
	}
	if p.Owner != "0xa1" {
		t.Errorf("Owner = %q", p.Owner)
	}
	if p.CollateralAsset != "0xweth" || p.DebtAsset != "0xusdc" {
		t.Errorf("assets = %q / %q", p.CollateralAsset, p.DebtAsset)
	}
	if p.HealthFactor != 1.02 {
		t.Errorf("HealthFactor = %v, want 1.02", p.HealthFactor)
	}
	// LLTV 0.86 → bonus bps = (1-0.86)*10000 = 1400
	if math.Abs(p.LiquidationBonus-1400) > 0.5 {
		t.Errorf("LiquidationBonus = %v, want ~1400", p.LiquidationBonus)
	}
	if p.DebtAmt != 2e9 {
		t.Errorf("DebtAmt = %v, want 2e9", p.DebtAmt)
	}
}

func TestMorphoDefaultLimitWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		if got := req.Variables["limit"]; got != float64(morphoDefaultLimit) {
			t.Errorf("limit = %v, want default %d", got, morphoDefaultLimit)
		}
		_, _ = io.WriteString(w, `{"data":{"marketPositions":{"items":[]}}}`)
	}))
	defer srv.Close()

	m := newMorphoClient(map[string]string{"morpho_url": srv.URL})
	got, err := m.fetchPositions(context.Background(), 8453, 0)
	if err != nil {
		t.Fatalf("fetchPositions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestMorphoGraphqlErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"unsupported chainId"}]}`)
	}))
	defer srv.Close()
	m := newMorphoClient(map[string]string{"morpho_url": srv.URL})
	_, err := m.fetchPositions(context.Background(), 8453, 10)
	if err == nil || !strings.Contains(err.Error(), "unsupported chainId") {
		t.Fatalf("err = %v", err)
	}
}

func TestMorphoHTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `boom`)
	}))
	defer srv.Close()
	m := newMorphoClient(map[string]string{"morpho_url": srv.URL})
	_, err := m.fetchPositions(context.Background(), 8453, 10)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestMorphoDefaultURL(t *testing.T) {
	m := newMorphoClient(nil)
	if m.graphqlURL != defaultMorphoGraphqlURL {
		t.Errorf("graphqlURL = %q, want default", m.graphqlURL)
	}
}
