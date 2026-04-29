package chain

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestYieldClientFetchMarginFi(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"banks":[
				{"address":"a","mintSymbol":"USDC","lendingApy":5.2,"borrowApy":7.1,"totalDeposits":1000000},
				{"address":"b","mintSymbol":"SOL","lendingApy":3.4,"borrowApy":6.0,"totalDeposits":250000},
				{"address":"c","mintSymbol":"","lendingApy":1,"borrowApy":1,"totalDeposits":1}
			]
		}`)
	}))
	defer srv.Close()

	y := newYieldClient(map[string]string{"marginfi_yield_url": srv.URL})
	got, err := y.fetch(context.Background(), []string{"marginfi"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rates, want 2 (empty mint symbol filtered)", len(got))
	}
	if got[0].Protocol != "marginfi" || got[0].Asset != "USDC" || got[0].APY != 5.2 {
		t.Errorf("USDC row wrong: %+v", got[0])
	}
}

func TestYieldClientFetchKamino(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"markets":[
				{"reserves":[
					{"symbol":"JitoSOL","supplyApy":7.5,"totalSupplyUsd":50000000},
					{"symbol":"USDC","supplyApy":4.1,"totalSupplyUsd":80000000}
				]},
				{"reserves":[
					{"symbol":"SOL","supplyApy":3.0,"totalSupplyUsd":20000000}
				]}
			]
		}`)
	}))
	defer srv.Close()

	y := newYieldClient(map[string]string{"kamino_yield_url": srv.URL})
	got, err := y.fetch(context.Background(), []string{"kamino"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rates = %d, want 3", len(got))
	}
	if got[2].Asset != "SOL" || got[2].Protocol != "kamino" {
		t.Errorf("third row wrong: %+v", got[2])
	}
}

func TestYieldClientUnknownProtocolSilentlyDropped(t *testing.T) {
	y := newYieldClient(nil)
	got, err := y.fetch(context.Background(), []string{"aave"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty for non-Solana protocol", got)
	}
}

func TestYieldClientHTTP500Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `down`)
	}))
	defer srv.Close()
	y := newYieldClient(map[string]string{"marginfi_yield_url": srv.URL})
	_, err := y.fetch(context.Background(), []string{"marginfi"})
	if err == nil {
		t.Fatal("expected error")
	}
}
