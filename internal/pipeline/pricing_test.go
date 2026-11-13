package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/pricing"
)

type plainSource struct{ listings []listing.Listing }

func (s *plainSource) Poll(context.Context, *core.Record) ([]listing.Listing, error) {
	return s.listings, nil
}

// fakeClassifier stands in for the LLM gateway: it files DDR5 ads under
// one per-GB item (reusing it once it is a candidate), "PC" ads as bundles,
// and fails every call while down is set.
type fakeClassifier struct {
	calls atomic.Int32
	down  atomic.Bool
	gb    map[string]float64 // title -> GB in the ad
}

func (f *fakeClassifier) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if f.down.Load() {
		http.Error(w, `{"error":{"message":"upstream timed out"}}`, http.StatusBadGateway)
		return
	}
	var req struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var in struct {
		Listing struct{ Title string } `json:"listing"`
		Items   []struct{ ID, Name string }
	}
	_ = json.Unmarshal([]byte(req.Messages[1].Content), &in)

	out := map[string]any{"verdict": "bundle", "reason": "a whole PC", "lines": []any{}}
	if gb, ok := f.gb[in.Listing.Title]; ok {
		ln := map[string]any{
			"item_id":  "",
			"new_item": map[string]any{"name": "DDR5 desktop memory (UDIMM)", "unit": "GB", "category": "memory", "description": "DDR5 desktop sticks"},
			"label":    in.Listing.Title, "quantity": 1, "amount": gb, "line_price": 0, "condition": "used",
		}
		for _, it := range in.Items {
			if it.Name == "DDR5 desktop memory (UDIMM)" {
				ln["item_id"] = it.ID
				ln["new_item"] = map[string]any{"name": "", "unit": "", "category": "", "description": ""}
			}
		}
		out = map[string]any{"verdict": "items", "reason": "memory", "lines": []any{ln}}
	}
	args, _ := json.Marshal(out)
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"tool_calls": []any{map[string]any{"function": map[string]any{"name": "file_listing", "arguments": string(args)}}},
	}}}})
}

func withPricing(t *testing.T, f *fakeClassifier) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	Pricing = &pricing.Service{
		LLM:           &pricing.LLM{URL: srv.URL, Key: "test"},
		ClassifyModel: "luna",
		CompsWindow:   30 * 24 * time.Hour,
		AnchorMaxAge:  10 * 24 * time.Hour,
	}
	t.Cleanup(func() { Pricing = nil })
}

var dealConds = []map[string]any{
	{"field": "title", "op": "contains", "value": "DDR5"},
	{"field": "deal.pct_below", "op": ">=", "value": 15},
	{"field": "deal.confidence", "op": "in", "value": "high,medium"},
}

// Listings a seeker wants are priced per GB across capacities; a repost of
// the same ad reuses the earlier answer (no model call) and doesn't count
// as a second comp; bundles aren't priced; and only the listing well under
// the comps alerts the deal seeker.
func TestPricingDealAlerts(t *testing.T) {
	f := &fakeClassifier{gb: map[string]float64{
		"DDR5 16GB": 16, "DDR5 32GB kit": 32, "DDR5 64GB kit": 64, "DDR5 2x16 6000": 32, "DDR5 48GB": 48,
		"DDR5 32GB cheap": 32,
	}}
	withPricing(t, f)
	fake := &plainSource{listings: []listing.Listing{
		{ExternalID: "1", Title: "DDR5 16GB", Price: 160},
		{ExternalID: "2", Title: "DDR5 32GB kit", Price: 320},
		{ExternalID: "3", Title: "DDR5 64GB kit", Price: 640},
		{ExternalID: "4", Title: "DDR5 2x16 6000", Price: 330},
		{ExternalID: "5", Title: "DDR5 48GB", Price: 470},
		{ExternalID: "6", Title: "Gaming PC with DDR5", Price: 900},
	}}
	app := newApp(t, fake)
	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "fake", "kind": "classified", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	plain := save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "DDR5", "kind": "any", "enabled": true,
		"conditions": []map[string]any{{"field": "title", "op": "contains", "value": "DDR5"}},
	})
	deals := save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "DDR5 deals", "kind": "any", "enabled": true, "conditions": dealConds,
	})
	ctx := context.Background()

	if _, err := PollSource(ctx, app, src); err != nil {
		t.Fatal(err)
	}
	fake.listings = []listing.Listing{
		{ExternalID: "7", Title: "DDR5 32GB kit", Price: 320},   // repost of 2
		{ExternalID: "8", Title: "DDR5 32GB cheap", Price: 250}, // $7.81/GB
	}
	if _, err := PollSource(ctx, app, src); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 7 {
		t.Errorf("model calls = %d, want 7 (the repost reuses listing 2's answer)", n)
	}

	items, _ := app.FindRecordsByFilter("items", "", "", 0, 0)
	if len(items) != 1 || items[0].GetString("unit") != "GB" {
		t.Fatalf("items = %d, want one per-GB item", len(items))
	}
	obs, _ := app.FindRecordsByFilter("price_observations", "", "", 0, 0)
	if len(obs) != 7 {
		t.Fatalf("observations = %d, want 7 (6 ads + the repost; the PC is a bundle)", len(obs))
	}

	pc, _ := app.FindFirstRecordByData("listings", "external_id", "6")
	if pricing.Status(pc) != pricing.StatusBundle {
		t.Errorf("PC status = %q, want bundle", pricing.Status(pc))
	}
	repost, _ := app.FindFirstRecordByData("listings", "external_id", "7")
	var attrs map[string]any
	_ = repost.UnmarshalJSONField("attrs", &attrs)
	deal, _ := attrs["deal"].(map[string]any)
	if deal == nil || deal["comps"].(float64) != 4 || deal["pct_below"].(float64) != 0 {
		t.Errorf("repost deal = %v, want 4 comps (its original is the same ad) and 0%% under", deal)
	}

	cheap, _ := app.FindFirstRecordByData("listings", "external_id", "8")
	_ = cheap.UnmarshalJSONField("attrs", &attrs)
	deal, _ = attrs["deal"].(map[string]any)
	if deal == nil || deal["typical"].(float64) != 10 || deal["pct_below"].(float64) != 21.9 || deal["confidence"] != "medium" {
		t.Fatalf("cheap deal = %v, want typical $10/GB from 5 comps, 21.9%% under, medium", deal)
	}

	dealAlerts, _ := app.FindRecordsByFilter("alerts", "seeker = {:s}", "", 0, 0, map[string]any{"s": deals.Id})
	if len(dealAlerts) != 1 || dealAlerts[0].GetString("listing") != cheap.Id {
		t.Fatalf("deal alerts = %d, want only the cheap kit", len(dealAlerts))
	}
	if body := dealAlerts[0].GetString("body"); !strings.Contains(body, "Deal: $7.81/GB vs typical $10.00/GB, 22% under (comps, 5 comps, medium)") {
		t.Errorf("deal alert body = %q", body)
	}
	plainAlerts, _ := app.FindRecordsByFilter("alerts", "seeker = {:s}", "", 0, 0, map[string]any{"s": plain.Id})
	if len(plainAlerts) != 8 {
		t.Errorf("plain alerts = %d, want 8 (pricing never filters seekers without deal conditions)", len(plainAlerts))
	}
}

// When classification fails the listing is still stored and alerts seekers
// that don't ask for a deal; the hourly retry prices it and then alerts the
// deal seeker it now matches. Here the estimate comes from the anchor alone.
func TestPricingRetryAlertsLate(t *testing.T) {
	f := &fakeClassifier{gb: map[string]float64{"DDR5 32GB": 32}}
	f.down.Store(true)
	withPricing(t, f)
	fake := &plainSource{listings: []listing.Listing{{ExternalID: "1", Title: "DDR5 32GB", Price: 240}}}
	app := newApp(t, fake)
	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "fake", "kind": "classified", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "DDR5", "kind": "any", "enabled": true,
		"conditions": []map[string]any{{"field": "title", "op": "contains", "value": "DDR5"}},
	})
	deals := save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "DDR5 deals", "kind": "any", "enabled": true, "conditions": dealConds,
	})
	item := save(t, app, "items", map[string]any{"name": "DDR5 desktop memory (UDIMM)", "name_key": "ddr5 desktop memory udimm", "unit": "GB"})
	save(t, app, "price_refs", map[string]any{
		"item": item.Id, "status": "ok", "low": 8, "typical": 10, "high": 12, "new_typical": 12.5, "n_new": 3, "n_used": 2,
	})
	ctx := context.Background()

	stats, err := PollSource(ctx, app, src)
	if err != nil || stats.Matched != 1 {
		t.Fatalf("poll: %+v %v, want the plain seeker's match despite the classifier being down", stats, err)
	}
	rec, _ := app.FindFirstRecordByData("listings", "external_id", "1")
	if pricing.Status(rec) != pricing.StatusPending {
		t.Fatalf("status = %q, want pending", pricing.Status(rec))
	}

	f.down.Store(false)
	n, err := RetryPricing(ctx, app)
	if err != nil || n != 1 {
		t.Fatalf("retry = %d, %v; want 1", n, err)
	}
	alerts, _ := app.FindRecordsByFilter("alerts", "seeker = {:s}", "", 0, 0, map[string]any{"s": deals.Id})
	if len(alerts) != 1 || !strings.Contains(alerts[0].GetString("body"), "25% under (anchor, 0 comps, medium)") {
		t.Fatalf("deal alerts after retry = %d %v", len(alerts), alerts)
	}
	// A second retry finds nothing pending and alerts nobody twice.
	if n, err := RetryPricing(ctx, app); err != nil || n != 0 {
		t.Fatalf("second retry = %d, %v", n, err)
	}
}

// Homes are never priced, and nothing is priced for a listing no seeker
// would want (only the deal conditions are left out of that check).
func TestPricingOnlyWhatSeekersWant(t *testing.T) {
	f := &fakeClassifier{gb: map[string]float64{}}
	withPricing(t, f)
	fake := &plainSource{listings: []listing.Listing{{ExternalID: "1", Title: "GPU", Price: 500}}}
	app := newApp(t, fake)
	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "fake", "kind": "classified", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	save(t, app, "seekers", map[string]any{"user": user.Id, "name": "DDR5 deals", "kind": "any", "enabled": true, "conditions": dealConds})
	if _, err := PollSource(context.Background(), app, src); err != nil {
		t.Fatal(err)
	}
	homes := save(t, app, "sources", map[string]any{
		"name": "homes", "type": "fake", "kind": "home", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	save(t, app, "seekers", map[string]any{"user": user.Id, "name": "all", "kind": "any", "enabled": true})
	fake.listings = []listing.Listing{{ExternalID: "h", Title: "House", Price: 500000}}
	if _, err := PollSource(context.Background(), app, homes); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

// A search's price cap doesn't limit what is priced: listings over the cap
// still become comps (so typical is the market, not the budget), but only
// listings under it alert.
func TestPricingIgnoresPriceCapForComps(t *testing.T) {
	f := &fakeClassifier{gb: map[string]float64{"DDR5 32GB kit": 32}}
	withPricing(t, f)
	fake := &plainSource{listings: []listing.Listing{{ExternalID: "1", Title: "DDR5 32GB kit", Price: 320}}}
	app := newApp(t, fake)
	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "fake", "kind": "classified", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "cheap DDR5", "kind": "any", "enabled": true,
		"conditions": []map[string]any{
			{"field": "title", "op": "contains", "value": "DDR5"},
			{"field": "price", "op": "<=", "value": 200},
		},
	})
	stats, err := PollSource(context.Background(), app, src)
	if err != nil || stats.Matched != 0 {
		t.Fatalf("poll: %+v %v, want no match over the cap", stats, err)
	}
	rec, _ := app.FindFirstRecordByData("listings", "external_id", "1")
	if pricing.Status(rec) != pricing.StatusOK {
		t.Fatalf("status = %q, want the over-cap listing priced as a comp", pricing.Status(rec))
	}
}
