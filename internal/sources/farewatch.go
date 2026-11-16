package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/plugin"
)

type farewatchSource struct{}

var farewatchTransport = http.DefaultTransport.(*http.Transport).Clone()

func (farewatchSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
		Watch   string `json:"watch"`
		Days    int    `json:"days"`
	}
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, fmt.Errorf("farewatch: bad config: %w", err)
	}
	if cfg.Days == 0 {
		cfg.Days = 14
	}
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/") + "/api/deals")
	if err != nil {
		return nil, fmt.Errorf("farewatch: bad URL: %w", err)
	}
	q := u.Query()
	q.Set("days", strconv.Itoa(cfg.Days))
	q.Set("limit", "200")
	if cfg.Watch != "" {
		q.Set("watch", cfg.Watch)
	}
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	client := &http.Client{Transport: farewatchTransport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("farewatch: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("farewatch: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Deals []map[string]any `json:"deals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("farewatch: decode: %w", err)
	}
	if body.Deals == nil {
		return nil, fmt.Errorf("farewatch: missing deals array")
	}
	out := make([]listing.Listing, 0, len(body.Deals))
	for _, attrs := range body.Deals {
		raw, err := json.Marshal(attrs)
		if err != nil {
			return nil, err
		}
		var d struct {
			ID           int64     `json:"id"`
			CreatedAt    time.Time `json:"created_at"`
			Origin       string    `json:"origin"`
			Destination  string    `json:"destination"`
			OutboundDate string    `json:"outbound_date"`
			ReturnDate   string    `json:"return_date"`
			PriceTotal   float64   `json:"price_total"`
			Airlines     []string  `json:"airlines"`
			Stops        *int      `json:"stops"`
			Reason       string    `json:"reason"`
			URL          string    `json:"google_flights_url"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("farewatch: deal: %w", err)
		}
		outbound, err := time.Parse("2006-01-02", d.OutboundDate)
		if err != nil {
			return nil, fmt.Errorf("farewatch: outbound date: %w", err)
		}
		back, err := time.Parse("2006-01-02", d.ReturnDate)
		if err != nil {
			return nil, fmt.Errorf("farewatch: return date: %w", err)
		}
		title := fmt.Sprintf("%s→%s %s–%s · $%s", d.Origin, d.Destination, outbound.Format("Jan 2"), back.Format("Jan 2"), message.NewPrinter(language.English).Sprintf("%.0f", d.PriceTotal))
		if len(d.Airlines) > 0 {
			attrs["airline"] = d.Airlines[0]
			title += " · " + d.Airlines[0]
		}
		if d.Stops != nil {
			stops := "nonstop"
			if *d.Stops == 1 {
				stops = "1 stop"
			} else if *d.Stops != 0 {
				stops = fmt.Sprintf("%d stops", *d.Stops)
			}
			if len(d.Airlines) > 0 {
				title += " "
			} else {
				title += " · "
			}
			title += stops
		}
		for k, v := range attrs {
			if v == nil {
				delete(attrs, k)
			}
		}
		out = append(out, listing.Listing{ExternalID: fmt.Sprintf("deal-%d", d.ID), Title: title, Price: d.PriceTotal, URL: d.URL, Published: d.CreatedAt, Description: d.Reason, Attrs: attrs})
	}
	return out, nil
}

func init() {
	Register(plugin.Info{
		Type: "farewatch", Label: "Farewatch flight deals", Description: "Flight deals from the farewatch service.",
		Fields: []plugin.Field{
			{Key: "base_url", Label: "Base URL", Type: plugin.URL, Required: true, Placeholder: "http://farewatch.farewatch", Hint: "Default service address: http://farewatch.farewatch"},
			{Key: "token", Label: "Token", Type: plugin.Secret},
			{Key: "watch", Label: "Watch", Type: plugin.Text, Hint: "Only this watch id (optional)"},
			{Key: "days", Label: "Days", Type: plugin.Number, Default: 14, Min: plugin.Float(1), Max: plugin.Float(90), Hint: "How far back to fetch deals"},
		},
	}, farewatchSource{})
}
