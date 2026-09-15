// Package notify sends operator alerts (Telegram).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Telegram posts messages through the Bot API.
type Telegram struct {
	Client *http.Client
	// Config returns (token, chatID); called per message so settings edits
	// take effect immediately.
	Config func(ctx context.Context) (string, string)
	Logger *slog.Logger

	mu       sync.Mutex
	lastSent map[string]time.Time
}

// New builds a Telegram notifier.
func New(cfg func(ctx context.Context) (string, string), logger *slog.Logger) *Telegram {
	return &Telegram{Client: &http.Client{Timeout: 15 * time.Second}, Config: cfg, Logger: logger, lastSent: map[string]time.Time{}}
}

// Send posts text (Markdown disabled for safety).
func (t *Telegram) Send(ctx context.Context, text string) error {
	token, chat := t.Config(ctx)
	if token == "" || chat == "" {
		return errors.New("telegram not configured")
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	return nil
}

// SendDedup sends at most once per key within window.
func (t *Telegram) SendDedup(ctx context.Context, key string, window time.Duration, text string) {
	t.mu.Lock()
	if last, ok := t.lastSent[key]; ok && time.Since(last) < window {
		t.mu.Unlock()
		return
	}
	t.lastSent[key] = time.Now()
	t.mu.Unlock()
	if err := t.Send(ctx, text); err != nil && t.Logger != nil && err.Error() != "telegram not configured" {
		t.Logger.Warn("telegram send failed", "err", err)
	}
}

// Test sends a probe message.
func (t *Telegram) Test(ctx context.Context) error {
	return t.Send(ctx, "✅ ctlvps 通知测试成功")
}
