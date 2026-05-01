package security

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newCapturingLogger wires the redacting core to an in-memory buffer so
// tests can assert on the JSON output. Mirrors NewLogger but ignores
// the level + sink defaults.
func newCapturingLogger(t *testing.T) (*zap.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	cfg := zap.NewProductionEncoderConfig()
	enc := zapcore.NewJSONEncoder(cfg)
	core := zapcore.NewCore(enc, zapcore.AddSync(buf), zapcore.DebugLevel)
	return zap.New(WrapCore(core)), buf
}

type logEntry struct {
	Msg          string  `json:"msg"`
	APIKey       string  `json:"api_key"`
	MasterKey    string  `json:"master_key"`
	ClientSecret string  `json:"client_secret"`
	AuthToken    string  `json:"auth_token"`
	WalletSeed   string  `json:"wallet_seed"`
	WalletAddr   string  `json:"wallet_address"`
	Signature    string  `json:"signature"`
	UserID       string  `json:"user_id"`
	Amount       float64 `json:"amount"`
}

func TestRedactsKeyVariants(t *testing.T) {
	l, buf := newCapturingLogger(t)
	l.Info("test",
		zap.String("api_key", "ak_live_abcdef123456"),
		zap.String("MASTER_KEY", "mk_super_secret"),
		zap.String("client_secret", "cs_topsecret"),
		zap.String("auth_token", "tok_xyz"),
		zap.String("wallet_seed", "0xprivatebytes"),
		zap.String("wallet_address", "0xabc..."),
		zap.String("signature", "0xdeadbeef"),
		zap.String("user_id", "u_42"),
		zap.Float64("amount", 1.23),
	)
	out := buf.String()
	for _, leaked := range []string{
		"ak_live_abcdef123456", "mk_super_secret", "cs_topsecret",
		"tok_xyz", "0xprivatebytes", "0xabc...", "0xdeadbeef",
	} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked secret %q in output: %s", leaked, out)
		}
	}
	// Non-secret fields must pass through intact.
	if !strings.Contains(out, `"user_id":"u_42"`) {
		t.Errorf("user_id was redacted unexpectedly: %s", out)
	}
	if !strings.Contains(out, `"amount":1.23`) {
		t.Errorf("amount was redacted unexpectedly: %s", out)
	}
	// Every redacted field replaced with the canonical value.
	if !strings.Contains(out, redactedValue) {
		t.Errorf("expected %q somewhere in output, got %s", redactedValue, out)
	}
}

func TestRedactedJSONIsParsable(t *testing.T) {
	l, buf := newCapturingLogger(t)
	l.Info("hello",
		zap.String("api_key", "secret"),
		zap.String("user_id", "u1"),
		zap.Float64("amount", 9.5),
	)
	var got logEntry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode log line: %v", err)
	}
	if got.APIKey != redactedValue {
		t.Errorf("APIKey = %q, want %q", got.APIKey, redactedValue)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q", got.UserID)
	}
	if got.Amount != 9.5 {
		t.Errorf("Amount = %v", got.Amount)
	}
}

func TestRedactsAcrossWith(t *testing.T) {
	// .With() bakes fields into the logger; redaction must apply there too.
	l, buf := newCapturingLogger(t)
	child := l.With(zap.String("session_token", "tok_abcd"))
	child.Info("ping")
	if strings.Contains(buf.String(), "tok_abcd") {
		t.Errorf("With-baked secret leaked: %s", buf.String())
	}
}

func TestRegexCaseInsensitive(t *testing.T) {
	cases := map[string]bool{
		"api_key":        true,
		"API_KEY":        true,
		"MasterKey":      true,
		"client_secret":  true,
		"AUTH_TOKEN":     true,
		"wallet_seed":    true,
		"Wallet_Address": true,
		"signature":      true,
		"Signature":      true,
		// Non-matches:
		"user_id":      false,
		"amount":       false,
		"tx_hash":      false,
		"block_number": false,
	}
	for name, want := range cases {
		got := SecretFieldRegex.MatchString(name)
		if got != want {
			t.Errorf("SecretFieldRegex(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNewLoggerLevelParsing(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", ""} {
		if _, err := NewLogger(level); err != nil {
			t.Errorf("NewLogger(%q): %v", level, err)
		}
	}
	if _, err := NewLogger("verbose"); err == nil {
		t.Error("NewLogger(verbose): want error")
	}
}
