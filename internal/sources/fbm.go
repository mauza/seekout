package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/plugin"
)

// fbmSource polls a Facebook Marketplace search or category page without
// logging in.
//
// With browser User-Agent and Sec-Fetch-* headers, Facebook serves the first
// page of up to 24 listings server-rendered inside <script type="application/json">
// Relay payloads: search URLs (/marketplace/<city>/search/?query=...) put them
// under feed_units.edges[].node.listing, while category browse URLs
// (/marketplace/<city>/vehicles?...) use marketplace_feed_stories.edges[].node.listing.
//
// Like KSL, only the first page is reachable without a browser. Facebook's
// creation_time_descend sort also mixes in broad category noise on sparse
// queries, so best_match (the default) or daysSinceListed=7 with seeker
// conditions is usually cleaner than pure newest-first.
//
// Detail-page enrichment (/marketplace/item/<id>/) is opt-in via config.enrich
// ("on") so searches with noisy results do not make unnecessary requests.
type fbmSource struct{}

type fbmConfig struct {
	URL    string `json:"url"`
	Enrich string `json:"enrich"`
}

var (
	fbmTransport   http.RoundTripper = http.DefaultTransport.(*http.Transport).Clone()
	fbmEnrichDelay                   = 1500 * time.Millisecond
)

var (
	fbmJSONScript  = regexp.MustCompile(`(?s)<script[^>]*type="application/json"[^>]*>(.*?)</script>`)
	fbmUnavailable = regexp.MustCompile(`(?i)(isn't available|no longer available|content isn't available)`)
	fbmPriceStrip  = regexp.MustCompile(`[^0-9.]`)
	fbmNonIdent    = regexp.MustCompile(`[^a-z0-9]+`)
)

type fbmPrice struct {
	Amount          string `json:"amount"`
	FormattedAmount string `json:"formatted_amount"`
}

type fbmItem struct {
	ID           string    `json:"id"`
	Title        string    `json:"marketplace_listing_title"`
	CustomTitle  string    `json:"custom_title"`
	CreationTime int64     `json:"creation_time"`
	ListingPrice *fbmPrice `json:"listing_price"`
	IsSold       bool      `json:"is_sold"`
	IsPending    bool      `json:"is_pending"`
	IsLive       bool      `json:"is_live"`
	CategoryID   string    `json:"marketplace_listing_category_id"`
	Delivery     []string  `json:"delivery_types"`
	Photo        *struct {
		Image *struct {
			URI string `json:"uri"`
		} `json:"image"`
	} `json:"primary_listing_photo"`
	Location *struct {
		ReverseGeocode *struct {
			City  string `json:"city"`
			State string `json:"state"`
		} `json:"reverse_geocode"`
	} `json:"location"`
	LocationText *struct {
		Text string `json:"text"`
	} `json:"location_text"`
	Seller *struct {
		Name string `json:"name"`
	} `json:"marketplace_listing_seller"`
	Subtitles []struct {
		Subtitle string `json:"subtitle"`
	} `json:"custom_sub_titles_with_rendering_flags"`
}

type fbmDetail struct {
	ID           string `json:"id"`
	CreationTime int64  `json:"creation_time"`
	Description  *struct {
		Text string `json:"text"`
	} `json:"redacted_description"`
	Condition string `json:"condition"`
	IsSold    *bool  `json:"is_sold"`
	IsPending *bool  `json:"is_pending"`
	IsLive    *bool  `json:"is_live"`
	Attrs     []struct {
		Name  string `json:"attribute_name"`
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"attribute_data"`
	// Vehicle fields.
	Dealership   string `json:"dealership_name"`
	Make         string `json:"vehicle_make_display_name"`
	Model        string `json:"vehicle_model_display_name"`
	Trim         string `json:"vehicle_trim_display_name"`
	FuelType     string `json:"vehicle_fuel_type"`
	Transmission string `json:"vehicle_transmission_type"`
	TitleStatus  string `json:"vehicle_title_status"`
	Exterior     string `json:"vehicle_exterior_color"`
	Interior     string `json:"vehicle_interior_color"`
	VehCondition string `json:"vehicle_condition"`
	SellerType   string `json:"vehicle_seller_type"`
	VIN          string `json:"vehicle_identification_number"`
	Odometer     *struct {
		Value *float64 `json:"value"`
		Unit  string   `json:"unit"`
	} `json:"vehicle_odometer_data"`
}

func (fbmSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg fbmConfig
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, fmt.Errorf("fbm: bad config: %w", err)
	}
	if err := fbmCheckURL(cfg.URL); err != nil {
		return nil, err
	}
	page, err := fbmFetch(ctx, cfg.URL)
	if err != nil {
		return nil, err
	}
	return fbmParseSearch(page)
}

// Enrich fetches a new listing's item page when config.enrich == "on", adding
// the full description, item attributes (condition, brand, ...), and vehicle
// fields. When enrich is off (default), it is a no-op.
func (fbmSource) Enrich(ctx context.Context, src *core.Record, l *listing.Listing) error {
	var cfg fbmConfig
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return fmt.Errorf("fbm: bad config: %w", err)
	}
	if cfg.Enrich != "on" {
		return nil
	}
	target := l.URL
	if target == "" && l.ExternalID != "" {
		target = "https://www.facebook.com/marketplace/item/" + l.ExternalID + "/"
	}
	if target == "" {
		return fmt.Errorf("fbm detail: missing listing URL")
	}
	if fbmEnrichDelay > 0 {
		t := time.NewTimer(fbmEnrichDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	page, err := fbmFetch(ctx, target)
	if err != nil {
		return fmt.Errorf("fbm detail %s: %w", l.ExternalID, err)
	}
	return fbmParseDetail(page, l)
}

// fbmCheckURL requires an https facebook.com/marketplace search or category
// URL, rejecting individual /marketplace/item/ pages.
func fbmCheckURL(raw string) error {
	u, err := url.Parse(raw)
	host := strings.ToLower(u.Host)
	if err != nil || u.Scheme != "https" || (host != "www.facebook.com" && host != "facebook.com") {
		return fmt.Errorf("fbm: url must be an https://www.facebook.com/marketplace/... page, got %q", raw)
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p != "/marketplace" && !strings.HasPrefix(p, "/marketplace/") {
		return fmt.Errorf("fbm: url path must start with /marketplace/, got %q", raw)
	}
	if p == "/marketplace/item" || strings.HasPrefix(p, "/marketplace/item/") {
		return fmt.Errorf("fbm: url must be a search or category page, not a single item URL: %q", raw)
	}
	return nil
}

func fbmFetch(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := (&http.Client{Transport: fbmTransport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("fbm: fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("fbm: read: %w", err)
	}
	if resp.Request != nil && resp.Request.URL != nil {
		p := resp.Request.URL.Path
		if strings.HasPrefix(p, "/login") || strings.HasPrefix(p, "/checkpoint") {
			return "", fmt.Errorf("fbm: redirected to %s — blocked or login required", p)
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("fbm: status 429: rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fbm: status %d", resp.StatusCode)
	}
	return string(body), nil
}

// fbmParseSearch extracts listings from Facebook's server-rendered Relay JSON
// scripts. Both search pages (feed_units) and category pages
// (marketplace_feed_stories) are handled. A page with neither feed structure
// returns an error instead of silently reporting zero listings.
func fbmParseSearch(page string) ([]listing.Listing, error) {
	var (
		out       []listing.Listing
		seen      = map[string]bool{}
		foundFeed bool
		parseErr  error
	)

	for _, m := range fbmJSONScript.FindAllStringSubmatch(page, -1) {
		raw := m[1]
		if !strings.Contains(raw, `"feed_units"`) && !strings.Contains(raw, `"marketplace_feed_stories"`) {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			continue
		}
		fbmWalkFeeds(doc, func(edges []any) {
			foundFeed = true
			for _, e := range edges {
				if parseErr != nil {
					return
				}
				em, _ := e.(map[string]any)
				node, _ := em["node"].(map[string]any)
				if node == nil {
					continue
				}
				if tn, _ := node["__typename"].(string); tn == "MarketplaceSearchFeedNoResults" {
					continue
				}
				rawItem, ok := node["listing"]
				if !ok || rawItem == nil {
					continue
				}
				b, err := json.Marshal(rawItem)
				if err != nil {
					parseErr = fmt.Errorf("fbm: marshal listing: %w", err)
					return
				}
				var it fbmItem
				if err := json.Unmarshal(b, &it); err != nil {
					parseErr = fmt.Errorf("fbm: decode listing: %w", err)
					return
				}
				title := fbmTitle(it)
				if it.ID == "" || title == "" {
					parseErr = fmt.Errorf("fbm: listing without id or title — layout likely changed")
					return
				}
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				out = append(out, fbmListing(it, title))
			}
		})
		if parseErr != nil {
			return nil, parseErr
		}
	}

	if !foundFeed {
		return nil, fmt.Errorf("fbm: no marketplace feed in page — blocked, login-walled, or layout changed")
	}
	return out, nil
}

func fbmWalkFeeds(v any, fn func(edges []any)) {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"feed_units", "marketplace_feed_stories"} {
			if feed, ok := x[key].(map[string]any); ok {
				if edges, ok := feed["edges"].([]any); ok {
					fn(edges)
				}
			}
		}
		for _, child := range x {
			fbmWalkFeeds(child, fn)
		}
	case []any:
		for _, child := range x {
			fbmWalkFeeds(child, fn)
		}
	}
}

func fbmTitle(it fbmItem) string {
	if s := strings.TrimSpace(it.Title); s != "" {
		return s
	}
	return strings.TrimSpace(it.CustomTitle)
}

func fbmParsePrice(p *fbmPrice) float64 {
	if p == nil {
		return 0
	}
	if s := strings.TrimSpace(p.Amount); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
			return v
		}
	}
	if s := fbmPriceStrip.ReplaceAllString(p.FormattedAmount, ""); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
			return v
		}
	}
	return 0
}

func fbmListing(it fbmItem, title string) listing.Listing {
	l := listing.Listing{
		ExternalID: it.ID,
		Title:      title,
		Price:      fbmParsePrice(it.ListingPrice),
		URL:        "https://www.facebook.com/marketplace/item/" + it.ID + "/",
		Attrs: map[string]any{
			"sold":    it.IsSold,
			"pending": it.IsPending,
			"live":    it.IsLive,
		},
	}
	if it.CreationTime > 0 {
		l.Published = time.Unix(it.CreationTime, 0).UTC()
	}
	if it.Photo != nil && it.Photo.Image != nil {
		l.Image = strings.TrimSpace(it.Photo.Image.URI)
	}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			l.Attrs[k] = v
		}
	}
	if it.Location != nil && it.Location.ReverseGeocode != nil {
		set("city", it.Location.ReverseGeocode.City)
		set("state", it.Location.ReverseGeocode.State)
	} else if it.LocationText != nil {
		if parts := strings.Split(it.LocationText.Text, ","); len(parts) == 2 {
			set("city", parts[0])
			set("state", parts[1])
		}
	}
	if it.Seller != nil {
		set("seller_name", it.Seller.Name)
	}
	set("category_id", it.CategoryID)
	if len(it.Delivery) > 0 {
		var delivery []string
		for _, d := range it.Delivery {
			if d = strings.TrimSpace(d); d != "" {
				delivery = append(delivery, d)
			}
		}
		if len(delivery) > 0 {
			l.Attrs["delivery_types"] = delivery
		}
	}
	if len(it.Subtitles) > 0 {
		var subs []string
		for _, s := range it.Subtitles {
			if t := strings.TrimSpace(s.Subtitle); t != "" {
				subs = append(subs, t)
			}
		}
		if len(subs) > 0 {
			l.Attrs["subtitles"] = subs
		}
	}
	return l
}

// fbmParseDetail adds description, item attributes, and vehicle fields from a
// listing's detail page. A removed/unavailable listing is marked unavailable
// so it does not retry forever; a blocked or mismatched page returns an error.
func fbmParseDetail(page string, l *listing.Listing) error {
	var found map[string]any
	for _, m := range fbmJSONScript.FindAllStringSubmatch(page, -1) {
		raw := m[1]
		if !strings.Contains(raw, `"redacted_description"`) {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			continue
		}
		found = fbmFindDetail(doc, l.ExternalID)
		if found != nil {
			break
		}
	}
	if found == nil {
		if fbmUnavailable.MatchString(page) {
			if l.Attrs == nil {
				l.Attrs = map[string]any{}
			}
			l.Attrs["unavailable"] = true
			return nil
		}
		return fmt.Errorf("fbm detail %s: no detail payload for item %s — blocked or layout changed", l.URL, l.ExternalID)
	}

	b, err := json.Marshal(found)
	if err != nil {
		return fmt.Errorf("fbm detail %s: %w", l.ExternalID, err)
	}
	var d fbmDetail
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("fbm detail %s: %w", l.ExternalID, err)
	}

	if d.Description != nil {
		l.Description = strings.TrimSpace(d.Description.Text)
	}
	if l.Published.IsZero() && d.CreationTime > 0 {
		l.Published = time.Unix(d.CreationTime, 0).UTC()
	}
	if l.Attrs == nil {
		l.Attrs = map[string]any{}
	}
	if d.IsSold != nil {
		l.Attrs["sold"] = *d.IsSold
	}
	if d.IsPending != nil {
		l.Attrs["pending"] = *d.IsPending
	}
	if d.IsLive != nil {
		l.Attrs["live"] = *d.IsLive
	}

	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			l.Attrs[k] = v
		}
	}
	set("condition", d.Condition)
	set("condition", d.VehCondition)
	for _, a := range d.Attrs {
		key := fbmSnakeKey(a.Name)
		if key == "" {
			continue
		}
		val := strings.TrimSpace(a.Label)
		if val == "" {
			val = strings.TrimSpace(a.Value)
		}
		set(key, val)
	}
	set("make", d.Make)
	set("model", d.Model)
	set("trim", d.Trim)
	set("fuel_type", d.FuelType)
	set("transmission", d.Transmission)
	set("title_status", d.TitleStatus)
	set("exterior_color", d.Exterior)
	set("interior_color", d.Interior)
	set("seller_type", d.SellerType)
	set("dealership_name", d.Dealership)
	set("vin", d.VIN)
	if d.Odometer != nil && d.Odometer.Value != nil {
		l.Attrs["mileage"] = *d.Odometer.Value
	}
	return nil
}

func fbmFindDetail(v any, wantID string) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		if id, _ := x["id"].(string); id == wantID {
			if _, ok := x["redacted_description"]; ok {
				return x
			}
		}
		for _, child := range x {
			if hit := fbmFindDetail(child, wantID); hit != nil {
				return hit
			}
		}
	case []any:
		for _, child := range x {
			if hit := fbmFindDetail(child, wantID); hit != nil {
				return hit
			}
		}
	}
	return nil
}

func fbmSnakeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = fbmNonIdent.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

func init() {
	Register(plugin.Info{
		Type:  "fbm",
		Label: "Facebook Marketplace",
		Description: "A facebook.com/marketplace search or category URL (logged-out, first page of up to 24 listings). " +
			"Use best_match (default) or daysSinceListed for sparse queries; keep intervals conservative (e.g. 60-120m).",
		Fields: []plugin.Field{
			{Key: "url", Label: "Search URL", Type: plugin.URL, Required: true,
				Hint:        "facebook.com/marketplace search or category URL (not a single /item/ page)",
				Placeholder: "https://www.facebook.com/marketplace/saltlakecity/search/?query=ddr5&daysSinceListed=7"},
			{Key: "enrich", Label: "Detail pages", Type: plugin.Select, Options: []string{"off", "on"}, Default: "off",
				Hint: "Fetch each new listing's page for description, condition and vehicle fields (more requests)"},
		},
	}, fbmSource{})
}
