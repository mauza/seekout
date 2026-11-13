package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Research limits.
const (
	maxQueries     = 3
	maxSources     = 10 // search results shown to the model
	maxFetch       = 5  // of which pages fetched
	pageTextMax    = 8000
	activeWindow   = 14 * 24 * time.Hour // researched only if observed this recently
	usedFromNew    = 0.8                 // used typical when research found only new prices
	browserUA      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36"
	researchSystem = `You extract prices from web pages for a deal tracker's reference prices.

You get one item (a price-comparison group priced per unit), example products recently listed under it, and numbered sources: search result snippets and page text.

Report every price in the sources for a product that belongs in the item. For each:
- source_id: the source it came from.
- quote: an exact, contiguous copy of the source text (at most 200 characters) that contains the price figure. Copy it character for character; it is checked against the source.
- price: the price in US dollars, exactly as the quote states it.
- amount: how many of the item's unit the priced product holds (a 2x16GB kit is 32 when the unit is GB; 1 when the unit is each). 0 if the source doesn't make it clear.
- condition: "used" for used, refurbished, open-box or sold-listing prices; otherwise "new".
- product: the product in a few words.

Report current offers and recent sold prices only. Skip price-history statistics (lows, highs, averages or medians over time), prices for other products or accessories, shipping, monthly payments, price-per-unit figures, and anything you'd have to compute or guess. It is fine to report nothing.`
)

var researchTool = Tool{
	Name:        "record_prices",
	Description: "Record the prices found in the sources.",
	Parameters: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"prices"},
		"properties": map[string]any{
			"prices": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"source_id", "quote", "price", "amount", "condition", "product"},
					"properties": map[string]any{
						"source_id": map[string]any{"type": "string"},
						"quote":     map[string]any{"type": "string"},
						"price":     map[string]any{"type": "number"},
						"amount":    map[string]any{"type": "number"},
						"condition": map[string]any{"type": "string", "enum": []string{"new", "used"}},
						"product":   map[string]any{"type": "string"},
					},
				},
			},
		},
	},
}

type source struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type foundPrice struct {
	SourceID  string  `json:"source_id"`
	Quote     string  `json:"quote"`
	Price     float64 `json:"price"`
	Amount    float64 `json:"amount"`
	Condition string  `json:"condition"`
	Product   string  `json:"product"`
}

// Citation is a verified price a reference was computed from.
type Citation struct {
	URL       string  `json:"url"`
	Title     string  `json:"title,omitempty"`
	Quote     string  `json:"quote"`
	Price     float64 `json:"price"`
	Amount    float64 `json:"amount"`
	UnitPrice float64 `json:"unit_price"`
	Condition string  `json:"condition"`
	Product   string  `json:"product"`
}

// ResearchDue researches the items seen in the last activeWindow whose
// last research is older than ResearchEvery, most recently seen first, up
// to ResearchMax. One item's failure doesn't stop the others.
func (s *Service) ResearchDue(ctx context.Context, app core.App) (int, error) {
	if s.SearxURL == "" {
		return 0, nil
	}
	now := s.now()
	// A little slack, so a daily cron at a fixed hour researches every
	// ResearchEvery days instead of drifting a day later each time.
	due := now.Add(-s.ResearchEvery + 2*time.Hour)
	items, err := app.FindRecordsByFilter("items",
		"last_observed >= {:active} && (last_researched = '' || last_researched <= {:due})", "-last_observed", s.ResearchMax, 0,
		dbx.Params{"active": fmtTime(now.Add(-activeWindow)), "due": fmtTime(due)})
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		// last_observed outlives observations a backfill --redo removed.
		if obs, err := app.FindRecordsByFilter("price_observations", "item = {:i} && observed_at >= {:active}", "", 1, 0,
			dbx.Params{"i": it.Id, "active": fmtTime(now.Add(-activeWindow))}); err != nil || len(obs) == 0 {
			continue
		}
		n++
		if _, err := s.Research(ctx, app, it); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", it.GetString("name"), err))
			app.Logger().Warn("pricing: research", "item", it.Id, "error", err)
		}
	}
	return n, errors.Join(errs...)
}

// Research searches the web for item's prices and stores a price_refs
// record: status ok with the anchor band, no_data when nothing verified, or
// error. The item's last_researched is set either way, so a failing item
// waits its turn instead of being retried every day.
func (s *Service) Research(ctx context.Context, app core.App, item *core.Record) (*core.Record, error) {
	col, err := app.FindCollectionByNameOrId("price_refs")
	if err != nil {
		return nil, err
	}
	ref := core.NewRecord(col)
	ref.Set("item", item.Id)
	ref.Set("model", s.ResearchModel)

	labels, err := recentLabels(app, item.Id, 2)
	if err != nil {
		return nil, err
	}
	queries := researchQueries(item, labels)
	ref.Set("queries", queries)

	cites, rejected, rerr := s.research(ctx, item, labels, queries)
	ref.Set("rejected", rejected)
	switch {
	case rerr != nil:
		ref.Set("status", "error")
		ref.Set("error", rerr.Error())
	default:
		band, ok := anchorBand(cites)
		ref.Set("citations", cites)
		if !ok {
			ref.Set("status", "no_data")
			break
		}
		ref.Set("status", "ok")
		ref.Set("low", band.Low)
		ref.Set("typical", band.Typical)
		ref.Set("high", band.High)
		ref.Set("new_typical", band.New)
		ref.Set("n_new", band.nNew)
		ref.Set("n_used", band.nUsed)
	}
	if err := app.Save(ref); err != nil {
		return nil, err
	}
	item.Set("last_researched", fmtTime(s.now()))
	if err := app.Save(item); err != nil {
		return ref, err
	}
	return ref, rerr
}

func (s *Service) research(ctx context.Context, item *core.Record, labels, queries []string) ([]Citation, int, error) {
	srcs, err := s.gather(ctx, queries)
	if err != nil {
		return nil, 0, err
	}
	if len(srcs) == 0 {
		return nil, 0, nil
	}
	in := map[string]any{
		"item": map[string]any{
			"name": item.GetString("name"), "unit": item.GetString("unit"),
			"category": item.GetString("category"), "description": item.GetString("description"),
		},
		"example_products": labels,
		"sources":          srcs,
	}
	user, err := json.MarshalIndent(in, "", " ")
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		Prices []foundPrice `json:"prices"`
	}
	if err := s.LLM.Call(ctx, s.ResearchModel, researchSystem, string(user), researchTool, &out); err != nil {
		return nil, 0, err
	}
	cites, rejected := verify(out.Prices, srcs, item.GetString("unit"))
	return cites, rejected, nil
}

// verify keeps the prices whose quote is really in the cited source and
// really states the price, with a usable amount.
func verify(found []foundPrice, srcs []source, unit string) ([]Citation, int) {
	byID := map[string]source{}
	for _, s := range srcs {
		byID[s.ID] = s
	}
	var out []Citation
	rejected := 0
	for _, f := range found {
		src, ok := byID[f.SourceID]
		amount := f.Amount
		if amount <= 0 && unit == "each" {
			amount = 1
		}
		if !ok || f.Price <= 0 || amount <= 0 || !quoteHolds(src.Text, f.Quote, f.Price) {
			rejected++
			continue
		}
		cond := f.Condition
		if cond != "used" {
			cond = "new"
		}
		out = append(out, Citation{
			URL: src.URL, Title: src.Title, Quote: collapse(f.Quote), Price: f.Price, Amount: amount,
			UnitPrice: f.Price / amount, Condition: cond, Product: f.Product,
		})
	}
	return out, rejected
}

type band struct {
	Anchor
	nNew, nUsed int
}

// anchorBand turns verified citations into the anchor: the used band from
// used prices when there are at least two, else derived from new retail
// (used ≈ usedFromNew × new). Unit prices more than 3x off the median are
// dropped first; they are almost always a misread amount.
func anchorBand(cites []Citation) (band, bool) {
	var all []float64
	for _, c := range cites {
		all = append(all, c.UnitPrice)
	}
	if len(all) == 0 {
		return band{}, false
	}
	med := median(all)
	var news, used []float64
	for _, c := range cites {
		if c.UnitPrice < med/3 || c.UnitPrice > med*3 {
			continue
		}
		if c.Condition == "used" {
			used = append(used, c.UnitPrice)
		} else {
			news = append(news, c.UnitPrice)
		}
	}
	b := band{nNew: len(news), nUsed: len(used)}
	if len(news) > 0 {
		b.New = median(news)
	}
	switch {
	case len(used) >= 2:
		b.Typical = median(used)
		b.Low = min(quantile(used, 0.1), b.Typical*0.85)
		b.High = max(quantile(used, 0.9), b.Typical*1.15)
	case len(news) > 0:
		b.Typical = b.New * usedFromNew
		b.Low = b.New * 0.6
		b.High = b.New
	default:
		return band{}, false
	}
	b.Low, b.Typical, b.High, b.New = roundPrice(b.Low), roundPrice(b.Typical), roundPrice(b.High), roundPrice(b.New)
	return b, true
}

// researchQueries searches for the item's price new and used, and for the
// product most listed under it (a specific product finds shop pages with
// prices). Without the used query, results are nearly all new retail.
func researchQueries(item *core.Record, labels []string) []string {
	qs := []string{item.GetString("name") + " price", item.GetString("name") + " used price"}
	for _, l := range labels {
		qs = append(qs, l+" price")
	}
	if len(qs) > maxQueries {
		qs = qs[:maxQueries]
	}
	return qs
}

// recentLabels are the n most common observation labels of the item over
// the last 30 days.
func recentLabels(app core.App, itemID string, n int) ([]string, error) {
	obs, err := app.FindRecordsByFilter("price_observations", "item = {:i}", "-observed_at", 200, 0, dbx.Params{"i": itemID})
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	var order []string
	for _, o := range obs {
		l := strings.TrimSpace(o.GetString("label"))
		if l == "" {
			continue
		}
		if count[l] == 0 {
			order = append(order, l)
		}
		count[l]++
	}
	slices.SortStableFunc(order, func(a, b string) int { return count[b] - count[a] })
	if len(order) > n {
		order = order[:n]
	}
	return order, nil
}

// gather runs the queries through SearXNG, keeps the first maxSources
// distinct results as snippet sources, and appends the page text of the
// first maxFetch that load.
func (s *Service) gather(ctx context.Context, queries []string) ([]source, error) {
	var srcs []source
	seen := map[string]bool{}
	var errs []error
	for _, q := range queries {
		results, err := s.searx(ctx, q)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range results {
			if seen[r.URL] || len(srcs) >= maxSources {
				continue
			}
			seen[r.URL] = true
			srcs = append(srcs, source{
				ID: fmt.Sprintf("S%d", len(srcs)+1), URL: r.URL, Title: r.Title, Text: collapse(r.Title + " — " + r.Content),
			})
		}
	}
	if len(srcs) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	fetched := 0
	for i := range srcs {
		if fetched >= maxFetch || ctx.Err() != nil {
			break
		}
		text, err := s.fetchPage(ctx, srcs[i].URL)
		if err != nil || text == "" {
			continue
		}
		fetched++
		srcs[i].Text += "\n" + text
	}
	return srcs, nil
}

type searxResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (s *Service) searx(ctx context.Context, q string) ([]searxResult, error) {
	u := strings.TrimRight(s.SearxURL, "/") + "/search?" + url.Values{"q": {q}, "format": {"json"}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req) // the configured internal SearXNG, not a user URL
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Results []searxResult `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	return out.Results, nil
}

// fetchPage loads a search result through the guarded client (the URL came
// from the web, so it must not reach internal addresses).
func (s *Service) fetchPage(ctx context.Context, raw string) (string, error) {
	if s.Fetch == nil {
		return "", errors.New("no fetch client")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("not an http URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := s.Fetch.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if err != nil {
		return "", err
	}
	return pageText(string(body), pageTextMax), nil
}
