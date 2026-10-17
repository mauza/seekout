package alerters

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/netguard"
	"github.com/mauza/seekout/internal/plugin"
)

type ntfyConfig struct {
	URL      string `json:"url"`      // base URL, default https://ntfy.sh
	Topic    string `json:"topic"`    // required
	Token    string `json:"token"`    // optional Bearer token for protected topics
	Priority string `json:"priority"` // optional 1-5
	Tags     string `json:"tags"`     // optional comma-separated
}

type ntfyAlerter struct{}

func (ntfyAlerter) Send(ctx context.Context, rec *core.Record, alert Alert) error {
	var cfg ntfyConfig
	if err := rec.UnmarshalJSONField("config", &cfg); err != nil {
		return fmt.Errorf("ntfy: bad config: %w", err)
	}
	if cfg.Topic == "" {
		return fmt.Errorf("ntfy: config.topic is required")
	}
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		base = "https://ntfy.sh"
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+cfg.Topic, strings.NewReader(alert.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", alert.Title)
	if cfg.Priority != "" {
		req.Header.Set("Priority", cfg.Priority)
	}
	if cfg.Tags != "" {
		req.Header.Set("Tags", cfg.Tags)
	}
	if alert.URL != "" {
		req.Header.Set("Click", alert.URL)
	}
	if alert.Image != "" {
		req.Header.Set("Attach", alert.Image)
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := netguard.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("ntfy: %s: %s", resp.Status, body)
	}
	return nil
}

func init() {
	Register(plugin.Info{
		Type:        "ntfy",
		Label:       "ntfy push",
		Description: "Push to a phone or desktop through an ntfy server (ntfy.sh or self-hosted).",
		Fields: []plugin.Field{
			{Key: "url", Label: "Server", Type: plugin.URL, Default: "https://ntfy.sh", Placeholder: "https://ntfy.sh",
				Hint: "defaults to https://ntfy.sh"},
			{Key: "topic", Label: "Topic", Type: plugin.Text, Required: true, Placeholder: "pick-something-unguessable"},
			{Key: "token", Label: "Token", Type: plugin.Secret, Hint: "optional"},
			{Key: "priority", Label: "Priority", Type: plugin.Text, Hint: "1–5"},
			{Key: "tags", Label: "Tags", Type: plugin.Text, Placeholder: "house", Hint: "comma separated"},
		},
	}, ntfyAlerter{})
}
