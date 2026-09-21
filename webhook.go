package agentboard

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// WebhookPayload is the JSON body sent to a webhook: the change event and,
// when the event concerns one task, that task's current state.
type WebhookPayload struct {
	Event Event `json:"event"`
	Task  *Task `json:"task,omitempty"`
}

// Webhook forwards board events to a URL you configure. It is the generic
// outgoing integration point: nothing in agentboard is tied to a particular
// chat, CI or ticketing product. Delivery is best effort and at most once:
// a failed POST is logged and not retried, and a slow endpoint may miss
// events (the board never waits for it). Treat the payload as a hint and
// re-read the API when you need the truth.
type Webhook struct {
	// URL receives a POST per event. Must be http or https.
	URL string
	// Secret, if set, signs each body: the X-Agentboard-Signature header is
	// "sha256=" followed by the hex HMAC-SHA256 of the raw body.
	Secret string
	// Client sends the requests. Default: a client with a 5 second timeout.
	Client *http.Client
	// Logger reports delivery failures. Default: slog.Default().
	Logger *slog.Logger
}

// SignWebhookBody returns the X-Agentboard-Signature header value for body:
// "sha256=" followed by the hex HMAC-SHA256 of body under secret.
func SignWebhookBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature reports whether header is the correct signature of
// body under secret, comparing in constant time. Receivers should call it on
// the raw request body before trusting a delivery.
func VerifyWebhookSignature(secret string, body []byte, header string) bool {
	return hmac.Equal([]byte(SignWebhookBody(secret, body)), []byte(header))
}

// ValidateWebhookURL reports whether raw is usable as a webhook URL.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("agentboard: webhook url %q must be an absolute http(s) URL", raw)
	}
	return nil
}

// Run delivers events from b until ctx is cancelled.
func (w *Webhook) Run(ctx context.Context, b *Board) { w.Start(ctx, b)() }

// Start subscribes to b's events before it returns, so no event that happens
// afterwards is missed, and delivers them in a background goroutine until ctx
// is cancelled. The returned function blocks until that goroutine has
// finished.
func (w *Webhook) Start(ctx context.Context, b *Board) (wait func()) {
	client, logger := w.Client, w.Logger
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	ch, cancel := b.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if err := w.deliver(ctx, client, b, ev); err != nil {
					logger.Warn("agentboard: webhook delivery failed", "err", err)
				}
			}
		}
	}()
	return func() { <-done }
}

func (w *Webhook) deliver(ctx context.Context, client *http.Client, b *Board, ev Event) error {
	p := WebhookPayload{Event: ev}
	if ev.TaskID != "" {
		if d, err := b.Task(ev.TaskID); err == nil {
			p.Task = &d.Task
		}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agentboard-webhook")
	req.Header.Set("X-Agentboard-Event", ev.Type)
	if w.Secret != "" {
		req.Header.Set("X-Agentboard-Signature", SignWebhookBody(w.Secret, body))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook %s answered %s", w.URL, resp.Status)
	}
	return nil
}
