package chain

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOneInchGetQuoteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.URL.Path, "/8453/quote") {
			t.Errorf("path = %q, want /8453/quote", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("src") != "0xeth" || q.Get("dst") != "0xusdc" || q.Get("amount") != "1000000000000000000" {
			t.Errorf("query mismatch: %v", q)
		}
		_, _ = io.WriteString(w, `{"dstAmount":"3500000000","gas":150000}`)
	}))
	defer srv.Close()

	a := newAggregatorClient(map[string]string{
		"oneinch_url":     srv.URL,
		"oneinch_api_key": "test-key",
	})
	got, raw, err := a.oneInchGetQuote(context.Background(), 8453, "0xeth", "0xusdc", "1000000000000000000")
	if err != nil {
		t.Fatalf("oneInchGetQuote: %v", err)
	}
	if got.DstAmount != "3500000000" {
		t.Errorf("DstAmount = %s", got.DstAmount)
	}
	if !strings.Contains(string(raw), "dstAmount") {
		t.Errorf("raw should include dstAmount: %s", raw)
	}
}

func TestOneInchBuildSwapSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/8453/swap") {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("from") != "0xfrom" || q.Get("slippage") != "1" {
			t.Errorf("query mismatch: %v", q)
		}
		_, _ = io.WriteString(w, `{
			"dstAmount":"3500000000",
			"tx":{"from":"0xfrom","to":"0xrouter","data":"0xdeadbeef","value":"0","gas":150000,"gasPrice":"1000000000"}
		}`)
	}))
	defer srv.Close()

	a := newAggregatorClient(map[string]string{"oneinch_url": srv.URL})
	got, err := a.oneInchBuildSwap(context.Background(), 8453, "0xeth", "0xusdc", "1000000000000000000", "0xfrom", 1.0)
	if err != nil {
		t.Fatalf("oneInchBuildSwap: %v", err)
	}
	if got.Tx.To != "0xrouter" || got.Tx.Data != "0xdeadbeef" {
		t.Errorf("tx fields = %+v", got.Tx)
	}
}

func TestOneInchBuildSwapEmptyTxRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"dstAmount":"1","tx":{"to":"","data":""}}`)
	}))
	defer srv.Close()
	a := newAggregatorClient(map[string]string{"oneinch_url": srv.URL})
	_, err := a.oneInchBuildSwap(context.Background(), 8453, "0xeth", "0xusdc", "1", "0xfrom", 1.0)
	if err == nil || !strings.Contains(err.Error(), "empty tx") {
		t.Fatalf("err = %v, want empty tx", err)
	}
}

func TestOneInchHTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"description":"Chain id is not supported"}`)
	}))
	defer srv.Close()
	a := newAggregatorClient(map[string]string{"oneinch_url": srv.URL})
	_, _, err := a.oneInchGetQuote(context.Background(), 84532, "0xeth", "0xusdc", "1")
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("err = %v, want status 400", err)
	}
}

func TestParaswapGetQuoteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prices" {
			t.Errorf("path = %q, want /prices", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"priceRoute":{"destAmount":"3490000000","gasCost":"160000"}}`)
	}))
	defer srv.Close()
	a := newAggregatorClient(map[string]string{"paraswap_url": srv.URL})
	got, _, err := a.paraswapGetQuote(context.Background(), 8453, "0xeth", "0xusdc", "1000000000000000000")
	if err != nil {
		t.Fatalf("paraswapGetQuote: %v", err)
	}
	if got.PriceRoute.DestAmount != "3490000000" {
		t.Errorf("DestAmount = %s", got.PriceRoute.DestAmount)
	}
}

func TestParaswapEmptyPriceRouteRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"priceRoute":{"destAmount":""}}`)
	}))
	defer srv.Close()
	a := newAggregatorClient(map[string]string{"paraswap_url": srv.URL})
	_, _, err := a.paraswapGetQuote(context.Background(), 8453, "0xeth", "0xusdc", "1")
	if err == nil || !strings.Contains(err.Error(), "empty priceRoute") {
		t.Fatalf("err = %v, want empty priceRoute", err)
	}
}
