package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/veermshah/cambrian/internal/redis"
)

// TelegramConfig wires everything the notifier needs. ChatID, BotToken
// and Subscribe are required; the rest fall back to spec defaults.
//
// The notifier reads BotToken / ChatID from env (TELEGRAM_BOT_TOKEN,
// TELEGRAM_CHAT_ID) when LoadEnv is true and the explicit fields are
// empty — handy for the production wiring in cmd/.
type TelegramConfig struct {
	BotToken   string
	ChatID     string
	BaseURL    string // override for tests; default "https://api.telegram.org"
	HTTPClient *http.Client

	// Subscribe joins the channels this notifier listens to. Production
	// passes a *redis.Redis client; tests pass *redis.FakeRedis.
	Subscribe redis.Client

	// Channels is the list of redis channels to subscribe to. Defaults
	// to the four chunk-21 channels (circuit_breaker, lifecycle,
	// epoch_completed, budget).
	Channels []string

	// Throttle config. Zero values trigger production defaults.
	HourlyCap         int
	Cooldown          time.Duration

	// DigestHour is the UTC hour (0-23) the daily digest fires. Spec
	// default 09:00 user-TZ — the wiring layer picks the TZ; this
	// struct stores UTC.
	DigestHour int
	// DigestProvider builds the DigestSummary on each scheduled tick.
	// nil ⇒ the digest is skipped (useful in tests that only exercise
	// the live-event path).
	DigestProvider func(ctx context.Context, at time.Time) (DigestSummary, error)

	Now    func() time.Time
	Logger *slog.Logger

	// LoadEnv tells the constructor to fall back to TELEGRAM_BOT_TOKEN
	// / TELEGRAM_CHAT_ID when the explicit fields are empty.
	LoadEnv bool
}

// TelegramNotifier subscribes to event channels and sends throttled
// Telegram messages. Construct with NewTelegramNotifier; start with
// Run(ctx) which blocks until ctx is cancelled.
type TelegramNotifier struct {
	cfg      TelegramConfig
	throttle *Throttle

	channels []string

	lastDigestDate string // YYYY-MM-DD of the most recent fired digest
}

// DefaultChannels are the four channels chunk 21 emits on.
var DefaultChannels = []string{
	"events:circuit_breaker",
	"events:lifecycle",
	"events:epoch_completed",
	"events:budget",
}

// channelToEventType maps a redis channel name to the event type the
// formatter understands. The lifecycle channel carries both agent
// spawn and kill events; the parsePayload helpers infer the specific
// action from the payload.
func channelToEventType(channel string, payload []byte) string {
	switch channel {
	case "events:circuit_breaker":
		return EventCircuitBreaker
	case "events:epoch_completed":
		return EventEpochCompleted
	case "events:budget":
		return EventBudgetWarning
	case "events:lifecycle":
		if m := parsePayload(payload); m != nil {
			if v, ok := m["action"].(string); ok {
				switch strings.ToLower(v) {
				case "killed", "kill":
					return EventAgentKilled
				case "spawned", "spawn":
					return EventAgentSpawned
				}
			}
		}
		return EventAgentSpawned
	case "events:treasury":
		return EventTreasuryLow
	}
	return channel
}

// NewTelegramNotifier validates dependencies and returns a notifier.
func NewTelegramNotifier(cfg TelegramConfig) (*TelegramNotifier, error) {
	if cfg.LoadEnv {
		if cfg.BotToken == "" {
			cfg.BotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		}
		if cfg.ChatID == "" {
			cfg.ChatID = os.Getenv("TELEGRAM_CHAT_ID")
		}
	}
	if cfg.BotToken == "" {
		return nil, errors.New("telegram: bot token required")
	}
	if cfg.ChatID == "" {
		return nil, errors.New("telegram: chat id required")
	}
	if cfg.Subscribe == nil {
		return nil, errors.New("telegram: redis client required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.telegram.org"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DigestHour < 0 || cfg.DigestHour > 23 {
		cfg.DigestHour = 9
	}
	channels := cfg.Channels
	if len(channels) == 0 {
		channels = append([]string(nil), DefaultChannels...)
	}
	t := NewThrottle(ThrottleConfig{
		HourlyCap: cfg.HourlyCap,
		Cooldown:  cfg.Cooldown,
		Now:       cfg.Now,
	})
	return &TelegramNotifier{cfg: cfg, throttle: t, channels: channels}, nil
}

// Run blocks until ctx is cancelled. It subscribes to the configured
// channels, forwards every admitted event to sendMessage, and fires
// the daily digest at DigestHour. Errors on send are logged but never
// kill the loop — a flaky Telegram is preferable to a silent agent.
func (n *TelegramNotifier) Run(ctx context.Context) error {
	if n == nil {
		return errors.New("telegram: nil notifier")
	}
	inbox, err := n.cfg.Subscribe.Subscribe(ctx, n.channels...)
	if err != nil {
		return fmt.Errorf("telegram: subscribe: %w", err)
	}
	digestTicker := time.NewTicker(time.Minute)
	defer digestTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-inbox:
			if !ok {
				return nil
			}
			n.handleEvent(ctx, msg)
		case <-digestTicker.C:
			n.maybeFireDigest(ctx)
		}
	}
}

// HandleEventForTest is the per-message body factored out for tests.
// Production code uses Run.
func (n *TelegramNotifier) HandleEventForTest(ctx context.Context, msg redis.Message) {
	n.handleEvent(ctx, msg)
}

func (n *TelegramNotifier) handleEvent(ctx context.Context, msg redis.Message) {
	etype := channelToEventType(msg.Channel, msg.Payload)
	allowed, reason := n.throttle.Allow(etype)
	if !allowed {
		n.log("telegram.throttled", "channel", msg.Channel, "type", etype, "reason", reason)
		return
	}
	body := Format(Event{Type: etype, Payload: msg.Payload, At: n.cfg.Now()})
	if err := n.SendMessage(ctx, body); err != nil {
		n.log("telegram.send_failed", "type", etype, "error", err.Error())
	}
}

// maybeFireDigest checks the wall clock and fires the digest once per
// day when DigestHour is reached. Idempotent within the same UTC day.
func (n *TelegramNotifier) maybeFireDigest(ctx context.Context) {
	if n.cfg.DigestProvider == nil {
		return
	}
	now := n.cfg.Now()
	today := now.UTC().Format("2006-01-02")
	if today == n.lastDigestDate {
		return
	}
	if now.UTC().Hour() < n.cfg.DigestHour {
		return
	}
	d, err := n.cfg.DigestProvider(ctx, now)
	if err != nil {
		n.log("telegram.digest_provider_failed", "error", err.Error())
		return
	}
	body := FormatDigest(d)
	// Daily digest deliberately bypasses the same-type cooldown via a
	// distinct event type; it still consumes a token from the hourly
	// bucket so a runaway digest can't blast the chat.
	allowed, reason := n.throttle.Allow(EventDailyDigest)
	if !allowed {
		n.log("telegram.digest_throttled", "reason", reason)
		return
	}
	if err := n.SendMessage(ctx, body); err != nil {
		n.log("telegram.digest_send_failed", "error", err.Error())
		return
	}
	n.lastDigestDate = today
}

// SendMessage POSTs the body to Telegram's sendMessage endpoint. Public
// so the orchestrator can fire one-shot messages outside the Run loop
// (e.g. boot banner) and the integration test can probe a real chat.
func (n *TelegramNotifier) SendMessage(ctx context.Context, body string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(n.cfg.BaseURL, "/"), n.cfg.BotToken)
	form := url.Values{}
	form.Set("chat_id", n.cfg.ChatID)
	form.Set("text", body)
	form.Set("parse_mode", "Markdown")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := n.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram: sendMessage status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	// Parse the API envelope so callers see Telegram-level rejection
	// even on 200 (the API returns {"ok": false, "description": "..."}
	// for rejected payloads with HTTP 200).
	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err == nil && !env.OK && env.Description != "" {
		return fmt.Errorf("telegram: api error: %s", env.Description)
	}
	return nil
}

func (n *TelegramNotifier) log(msg string, kv ...any) {
	if n.cfg.Logger == nil {
		return
	}
	n.cfg.Logger.Info(msg, kv...)
}
