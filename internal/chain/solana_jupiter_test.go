package chain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJupiterGetQuoteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/quote" {
			t.Errorf("path = %s, want /quote", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("inputMint") != "So11111111111111111111111111111111111111112" {
			t.Errorf("inputMint = %s", q.Get("inputMint"))
		}
		if q.Get("outputMint") != "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v" {
			t.Errorf("outputMint = %s", q.Get("outputMint"))
		}
		if q.Get("amount") != "1000000000" {
			t.Errorf("amount = %s, want 1000000000", q.Get("amount"))
		}
		if q.Get("slippageBps") != "75" {
			t.Errorf("slippageBps = %s, want 75", q.Get("slippageBps"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"inputMint":"So11111111111111111111111111111111111111112",
			"outputMint":"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
			"inAmount":"1000000000",
			"outAmount":"150000000",
			"otherAmountThreshold":"148500000",
			"swapMode":"ExactIn",
			"slippageBps":75,
			"priceImpactPct":"0.001"
		}`)
	}))
	defer srv.Close()

	j := newJupiterClient(srv.URL)
	got, raw, err := j.getQuote(context.Background(),
		"So11111111111111111111111111111111111111112",
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		1_000_000_000, 75)
	if err != nil {
		t.Fatalf("getQuote: %v", err)
	}
	if got.OutAmount != "150000000" {
		t.Fatalf("OutAmount = %s, want 150000000", got.OutAmount)
	}
	if !strings.Contains(string(raw), "outAmount") {
		t.Fatalf("raw body should include outAmount, got %s", string(raw))
	}
}

func TestJupiterGetQuoteAppliesDefaultSlippage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slippageBps") != "50" {
			t.Errorf("slippageBps = %s, want default 50", r.URL.Query().Get("slippageBps"))
		}
		_, _ = io.WriteString(w, `{"inAmount":"1","outAmount":"1","slippageBps":50}`)
	}))
	defer srv.Close()
	j := newJupiterClient(srv.URL)
	if _, _, err := j.getQuote(context.Background(), "A", "B", 1, 0); err != nil {
		t.Fatalf("getQuote: %v", err)
	}
}

func TestJupiterGetQuoteNon200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad mint"}`)
	}))
	defer srv.Close()
	j := newJupiterClient(srv.URL)
	_, _, err := j.getQuote(context.Background(), "A", "B", 1, 50)
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("err = %v, want status 400", err)
	}
}

func TestJupiterBuildSwapTxSuccess(t *testing.T) {
	rawTx := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	encoded := base64.StdEncoding.EncodeToString(rawTx)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/swap" {
			t.Errorf("method/path = %s %s", r.Method, r.URL.Path)
		}
		var body jupiterSwapReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.UserPublicKey != "alice" {
			t.Errorf("UserPublicKey = %s", body.UserPublicKey)
		}
		if !body.WrapAndUnwrapSol || !body.DynamicComputeUnitLimit {
			t.Errorf("flags not set: %+v", body)
		}
		if !strings.Contains(string(body.QuoteResponse), "outAmount") {
			t.Errorf("QuoteResponse not forwarded: %s", string(body.QuoteResponse))
		}
		_, _ = io.WriteString(w, `{"swapTransaction":"`+encoded+`"}`)
	}))
	defer srv.Close()

	j := newJupiterClient(srv.URL)
	tx, err := j.buildSwapTx(context.Background(), "alice", []byte(`{"outAmount":"1","inAmount":"1"}`))
	if err != nil {
		t.Fatalf("buildSwapTx: %v", err)
	}
	if string(tx) != string(rawTx) {
		t.Fatalf("tx bytes = %x, want %x", tx, rawTx)
	}
}

func TestJupiterBuildSwapTxEmptyTransactionRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"swapTransaction":""}`)
	}))
	defer srv.Close()
	j := newJupiterClient(srv.URL)
	_, err := j.buildSwapTx(context.Background(), "alice", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "empty swapTransaction") {
		t.Fatalf("err = %v, want empty swapTransaction", err)
	}
}

func TestJupiterBuildSwapTxNon200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"down"}`)
	}))
	defer srv.Close()
	j := newJupiterClient(srv.URL)
	_, err := j.buildSwapTx(context.Background(), "alice", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v, want status 500", err)
	}
}
