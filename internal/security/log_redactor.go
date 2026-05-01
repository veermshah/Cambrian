package security

import (
	"fmt"
	"os"
	"regexp"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// redactedValue is what every redacted field's value gets replaced with
// before it reaches the underlying encoder. Constant so log searches
// can grep for the exact string.
const redactedValue = "<redacted>"

// SecretFieldRegex matches any field name that is likely to carry a
// private key, signed transaction, session token, or API secret. Spec
// line 499 says no private keys in logs; this is the broadest catch the
// rest of the codebase can rely on without thinking. Case-insensitive.
//
//   ^(.*key|.*secret|.*token|wallet.*|signature)$
//
// Examples that match: "api_key", "MASTER_KEY", "client_secret",
// "auth_token", "wallet_address", "wallet_seed", "signature".
// Examples that don't: "user_id", "amount", "tx_hash" (use "signature"
// for signed tx envelopes — call sites must not name the field "tx_hash"
// when the value is a private signature).
var SecretFieldRegex = regexp.MustCompile(`(?i)^(.*key|.*secret|.*token|wallet.*|signature)$`)

// NewLogger returns a JSON zap.Logger configured with the redacting
// core. level is one of "debug", "info", "warn", "error" (case-insensitive);
// any other value falls back to info.
func NewLogger(level string) (*zap.Logger, error) {
	cfg := zap.NewProductionEncoderConfig()
	cfg.TimeKey = "ts"
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	enc := zapcore.NewJSONEncoder(cfg)

	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	core := zapcore.NewCore(enc, zapcore.Lock(zapcore.AddSync(os.Stderr)), lvl)
	return zap.New(WrapCore(core), zap.AddCaller()), nil
}

// WrapCore returns a zapcore.Core that redacts secret fields before
// forwarding to the wrapped core. Exposed separately so tests (and the
// orchestrator's structured-log wiring) can build their own loggers
// around any encoder/sink combination.
func WrapCore(c zapcore.Core) zapcore.Core {
	return &redactCore{Core: c}
}

type redactCore struct {
	zapcore.Core
}

func (r *redactCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactCore{Core: r.Core.With(redactFields(fields))}
}

func (r *redactCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if r.Enabled(ent.Level) {
		return ce.AddCore(ent, r)
	}
	return ce
}

func (r *redactCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return r.Core.Write(ent, redactFields(fields))
}

func (r *redactCore) Sync() error { return r.Core.Sync() }

func redactFields(in []zapcore.Field) []zapcore.Field {
	if len(in) == 0 {
		return in
	}
	out := make([]zapcore.Field, len(in))
	for i, f := range in {
		if SecretFieldRegex.MatchString(f.Key) {
			out[i] = zap.String(f.Key, redactedValue)
			continue
		}
		out[i] = f
	}
	return out
}

func parseLevel(level string) (zapcore.Level, error) {
	switch level {
	case "", "info", "INFO", "Info":
		return zapcore.InfoLevel, nil
	case "debug", "DEBUG", "Debug":
		return zapcore.DebugLevel, nil
	case "warn", "WARN", "Warn", "warning", "WARNING":
		return zapcore.WarnLevel, nil
	case "error", "ERROR", "Error":
		return zapcore.ErrorLevel, nil
	default:
		return 0, fmt.Errorf("security: unknown log level %q", level)
	}
}
