package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/veermshah/cambrian/internal/notifications"
	"github.com/veermshah/cambrian/internal/redis"
)

// TestTelegram_EveryEventTypeDelivers — spec line 1196.
// Gated by E2E=1 + TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID. Issues one
// real Telegram message per event type so the operator can confirm
// formatting + emoji + truncation in a live chat.
//
// This test does send messages to your actual chat — only run it when
// you want a live notification probe.
func TestTelegram_EveryEventTypeDelivers(t *testing.T) {
	tok, chat := requireTelegram(t)
	n, err := notifications.NewTelegramNotifier(notifications.TelegramConfig{
		BotToken:  tok,
		ChatID:    chat,
		Subscribe: redis.NewFake(),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{
		"Cambrian e2e: circuit breaker probe",
		"Cambrian e2e: agent killed probe",
		"Cambrian e2e: agent spawned probe",
		"Cambrian e2e: epoch completed probe",
		"Cambrian e2e: budget warning probe",
		"Cambrian e2e: treasury low probe",
		"Cambrian e2e: daily digest probe",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, body := range events {
		if err := n.SendMessage(ctx, body); err != nil {
			t.Errorf("send %q: %v", body, err)
		}
	}
}
