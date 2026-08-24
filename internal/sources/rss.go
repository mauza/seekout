package sources

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"github.com/mmcdole/gofeed"
	"github.com/pocketbase/pocketbase/core"

	"gitlab.mauza.net/mauza/seekout/internal/listing"
)

type rssConfig struct {
	URL string `json:"url"`
}

// rssSource covers any RSS/Atom/RDF feed: Craigslist search feeds
// (https://<city>.craigslist.org/search/cta?format=rss&query=...), KSL
// alerts feeds, blog/classified feeds, etc.
type rssSource struct{}

var priceRe = regexp.MustCompile(`\$\s?([0-9][0-9,]*(?:\.[0-9]+)?)`)

func (rssSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg rssConfig
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, fmt.Errorf("rss: bad config: %w", err)
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("rss: config.url is required")
	}
	// Craigslist and friends 403 the default Gofeed UA.
	parser := gofeed.NewParser()
	parser.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	feed, err := parser.ParseURLWithContext(cfg.URL, ctx)
	if err != nil {
		return nil, fmt.Errorf("rss: fetch %s: %w", cfg.URL, err)
	}

	out := make([]listing.Listing, 0, len(feed.Items))
	for _, item := range feed.Items {
		l := listing.Listing{
			ExternalID:  firstNonEmpty(item.GUID, item.Link),
			Title:       strings.TrimSpace(item.Title),
			URL:         item.Link,
			Description: truncate(strings.TrimSpace(item.Description), 500),
			Attrs: map[string]any{
				"feed":       feed.Title,
				"categories": item.Categories,
			},
		}
		if l.ExternalID == "" {
			continue
		}
		if item.PublishedParsed != nil {
			l.Published = *item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			l.Published = *item.UpdatedParsed
		}
		if item.Image != nil {
			l.Image = item.Image.URL
		} else {
			for _, enc := range item.Enclosures {
				if strings.HasPrefix(enc.Type, "image/") {
					l.Image = enc.URL
					break
				}
			}
		}
		if m := priceRe.FindStringSubmatch(item.Title); m != nil {
			if price, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64); err == nil {
				l.Price = price
			}
		}
		out = append(out, l)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func init() {
	Register("rss", rssSource{})
}
