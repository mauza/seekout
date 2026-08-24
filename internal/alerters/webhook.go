package alerters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

type webhookConfig struct {
	URL     string            `json:"url"`     // required
	Headers map[string]string `json:"headers"` // optional extra headers
}

// webhookAlerter POSTs the alert as JSON. The "text" field makes it
// compatible with Slack incoming webhooks out of the box.
type webhookAlerter struct{}

func (webhookAlerter) Send(ctx context.Context, rec *core.Record, alert Alert) error {
	var cfg webhookConfig
	if err := rec.UnmarshalJSONField("config", &cfg); err != nil {
		return fmt.Errorf("webhook: bad config: %w", err)
	}
	if cfg.URL == "" {
		return fmt.Errorf("webhook: config.url is required")
	}

	payload := map[string]any{
		"title": alert.Title,
		"body":  alert.Body,
		"url":   alert.URL,
		"image": alert.Image,
		"text":  fmt.Sprintf("%s\n%s\n%s", alert.Title, alert.Body, alert.URL),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("webhook: %s: %s", resp.Status, respBody)
	}
	return nil
}

func init() {
	Register("webhook", webhookAlerter{})
}
