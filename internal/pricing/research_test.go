package pricing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	_ "github.com/mauza/seekout/migrations"
)

// Research searches, fetches result pages, keeps only the prices whose
// quotes are really in the sources, and stores the anchor.
func TestResearchVerifiesCitations(t *testing.T) {
	var shop *httptest.Server
	shop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Corsair 2x16GB DDR5-6000</h1><p>Our price: $119.99</p>
			<p>Used, tested: $90.00</p><p>G.Skill 2x32GB kit $239.99</p></body></html>`))
	}))
	t.Cleanup(shop.Close)
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			http.Error(w, "json only", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
			map[string]any{"url": shop.URL + "/p/1", "title": "Corsair DDR5", "content": "Corsair 2x16GB from $119.99"},
			map[string]any{"url": "https://example.invalid/2", "title": "eBay", "content": "Sold: 32GB DDR5 kit $85.00 used"},
		}})
	}))
	t.Cleanup(searx.Close)

	var sawSources []source
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string                     `json:"model"`
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var in struct{ Sources []source }
		_ = json.Unmarshal([]byte(req.Messages[1].Content), &in)
		sawSources = in.Sources
		args, _ := json.Marshal(map[string]any{"prices": []any{
			map[string]any{"source_id": "S1", "quote": "Our price: $119.99", "price": 119.99, "amount": 32, "condition": "new", "product": "Corsair 2x16"},
			map[string]any{"source_id": "S1", "quote": "Used, tested: $90.00", "price": 90, "amount": 32, "condition": "used", "product": "Corsair 2x16"},
			map[string]any{"source_id": "S1", "quote": "G.Skill 2x32GB kit $239.99", "price": 239.99, "amount": 64, "condition": "new", "product": "G.Skill 2x32"},
			map[string]any{"source_id": "S2", "quote": "Sold: 32GB DDR5 kit $85.00 used", "price": 85, "amount": 32, "condition": "used", "product": "32GB kit"},
			// Invented: not in any source.
			map[string]any{"source_id": "S2", "quote": "32GB DDR5 kit $20.00", "price": 20, "amount": 32, "condition": "used", "product": "fake"},
			// Wrong source id.
			map[string]any{"source_id": "S9", "quote": "Our price: $119.99", "price": 119.99, "amount": 32, "condition": "new", "product": "x"},
		}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"tool_calls": []any{map[string]any{"function": map[string]any{"name": "record_prices", "arguments": string(args)}}},
		}}}})
	}))
	t.Cleanup(llm.Close)

	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	item := saveRec(t, app, "items", map[string]any{"name": "DDR5 desktop memory (UDIMM)", "name_key": "ddr5 desktop memory udimm", "unit": "GB",
		"last_observed": fmtTime(time.Now())})
	src := saveRec(t, app, "sources", map[string]any{"name": "s", "type": "rss", "kind": "classified", "interval_minutes": 30,
		"config": map[string]any{"url": "https://example.test/feed"}})
	l := saveRec(t, app, "listings", map[string]any{"source": src.Id, "external_id": "1", "title": "Corsair 2x16GB DDR5-6000", "price": 300})
	saveRec(t, app, "price_observations", map[string]any{"listing": l.Id, "item": item.Id, "label": "Corsair 2x16GB DDR5-6000",
		"quantity": 1, "amount": 32, "total_price": 300, "unit_price": 9.375, "observed_at": fmtTime(time.Now())})
	// An item whose observations are gone (a backfill --redo) isn't researched.
	saveRec(t, app, "items", map[string]any{"name": "Orphan", "name_key": "orphan", "unit": "each", "last_observed": fmtTime(time.Now())})

	s := &Service{
		LLM: &LLM{URL: llm.URL, Key: "k"}, ResearchModel: "sol", SearxURL: searx.URL,
		ResearchEvery: 4 * 24 * time.Hour, ResearchMax: 5, Fetch: http.DefaultClient,
	}
	n, err := s.ResearchDue(context.Background(), app)
	if err != nil || n != 1 {
		t.Fatalf("ResearchDue = %d, %v", n, err)
	}
	if len(sawSources) != 2 || !strings.Contains(sawSources[0].Text, "Used, tested: $90.00") {
		t.Fatalf("sources given to the model = %+v, want the fetched page text appended", sawSources)
	}
	refs, _ := app.FindRecordsByFilter("price_refs", "", "", 0, 0)
	if len(refs) != 1 {
		t.Fatalf("refs = %d", len(refs))
	}
	ref := refs[0]
	if ref.GetString("status") != "ok" || ref.GetInt("rejected") != 2 || ref.GetInt("n_used") != 2 || ref.GetInt("n_new") != 2 {
		t.Fatalf("ref = %v", ref.PublicExport())
	}
	// used 90/32 and 85/32 → typical 2.7344; new 119.99/32 and 239.99/64 → 3.7498
	if got := ref.GetFloat("typical"); got < 2.73 || got > 2.74 {
		t.Errorf("typical = %v", got)
	}
	if got := ref.GetFloat("new_typical"); got < 3.74 || got > 3.76 {
		t.Errorf("new = %v", got)
	}
	// Researched now, so not due again for 4 days.
	if n, _ := s.ResearchDue(context.Background(), app); n != 0 {
		t.Errorf("researched again right away: %d", n)
	}
}

func saveRec(t *testing.T, app core.App, collection string, fields map[string]any) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	for k, v := range fields {
		rec.Set(k, v)
	}
	if err := app.Save(rec); err != nil {
		t.Fatalf("save %s: %v", collection, err)
	}
	return rec
}
