package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"gitlab.mauza.net/mauza/seekout/internal/listing"
	"gitlab.mauza.net/mauza/seekout/internal/sources"
	_ "gitlab.mauza.net/mauza/seekout/migrations"
)

// enrichingSource returns fixed listings and fails Enrich for ids in failing.
type enrichingSource struct {
	listings []listing.Listing
	failing  map[string]bool
	enriched []string
}

func (s *enrichingSource) Poll(context.Context, *core.Record) ([]listing.Listing, error) {
	return s.listings, nil
}

func (s *enrichingSource) Enrich(_ context.Context, _ *core.Record, l *listing.Listing) error {
	s.enriched = append(s.enriched, l.ExternalID)
	if s.failing[l.ExternalID] {
		return errors.New("detail page timed out")
	}
	l.Attrs["garage_capacity"] = 3
	return nil
}

func save(t *testing.T, app core.App, collection string, fields map[string]any) *core.Record {
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

// A listing whose enrichment fails is not stored, so the next poll retries
// it; the failure shows on the source; enrichment runs only for listings
// not stored yet; and enriched attrs are what seekers match on.
func TestPollEnrichment(t *testing.T) {
	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	fake := &enrichingSource{
		listings: []listing.Listing{
			{ExternalID: "a", Title: "a", Attrs: map[string]any{}},
			{ExternalID: "b", Title: "b", Attrs: map[string]any{}},
		},
		failing: map[string]bool{"b": true},
	}
	// The sources.type select only admits registered names, so the fake
	// stands in for "rss" in this test binary.
	sources.Register("rss", fake)

	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "rss", "kind": "home", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	seeker := save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "3-car", "kind": "any", "enabled": true,
		"conditions": []map[string]any{{"field": "garage_capacity", "op": ">=", "value": 3}},
	})
	ctx := context.Background()

	stats, err := PollSource(ctx, app, src)
	if err == nil || !strings.Contains(err.Error(), "1 new listing(s) skipped") {
		t.Fatalf("poll 1 err = %v, want the skipped-listing error", err)
	}
	if stats.New != 1 || stats.Matched != 1 {
		t.Fatalf("poll 1 stats = %+v, want 1 new / 1 matched", stats)
	}
	if src.GetString("last_error") == "" {
		t.Fatal("poll 1: last_error not recorded on the source")
	}
	if _, err := app.FindFirstRecordByData("listings", "external_id", "b"); err == nil {
		t.Fatal("listing b stored despite failed enrichment")
	}

	fake.failing = nil
	fake.enriched = nil
	stats, err = PollSource(ctx, app, src)
	if err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if stats.New != 1 || stats.Matched != 1 || src.GetString("last_error") != "" {
		t.Fatalf("poll 2 stats = %+v last_error = %q, want b stored and matched, error cleared", stats, src.GetString("last_error"))
	}
	if len(fake.enriched) != 1 || fake.enriched[0] != "b" {
		t.Fatalf("poll 2 enriched %v, want only the unstored listing b", fake.enriched)
	}
	alerts, err := app.FindRecordsByFilter("alerts", "seeker = {:s}", "", 0, 0, map[string]any{"s": seeker.Id})
	if err != nil || len(alerts) != 2 {
		t.Fatalf("alerts = %d (%v), want 2 (a and b)", len(alerts), err)
	}
	if body := alerts[0].GetString("body"); strings.Contains(body, "score") {
		t.Errorf("hard-only seeker alert body has a score line: %q", body)
	}
}

// A soft seeker's alert says what traded off.
func TestSoftSeekerAlertExplains(t *testing.T) {
	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	sources.Register("rss", &enrichingSource{listings: []listing.Listing{
		{ExternalID: "x", Title: "12 Main St Lehi, UT", Price: 700000, Attrs: map[string]any{"lot_acres": 0.4}},
	}})
	user := save(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	src := save(t, app, "sources", map[string]any{
		"name": "fake", "type": "rss", "kind": "home", "interval_minutes": 30, "enabled": true, "seeded": true,
	})
	save(t, app, "seekers", map[string]any{
		"user": user.Id, "name": "soft", "kind": "any", "enabled": true,
		"conditions": []map[string]any{
			// enrichingSource sets garage_capacity 3; 2 would be the floor.
			{"field": "garage_capacity", "op": ">=", "value": 4, "soft": true, "floor": 2, "best": 5, "weight": 2},
			{"field": "lot_acres", "op": ">=", "value": 0.15, "soft": true, "floor": 0.12, "best": 0.4},
		},
	})
	stats, err := PollSource(context.Background(), app, src)
	if err != nil || stats.Matched != 1 {
		t.Fatalf("poll: %+v %v, want 1 match (garage -0.5 x2 + lot +1 = 0)", stats, err)
	}
	alert, err := app.FindFirstRecordByData("alerts", "title", "12 Main St Lehi, UT")
	if err != nil {
		t.Fatal(err)
	}
	want := "12 Main St Lehi, UT — $700000\nscore +0.00: garage_capacity 3 (-0.50 x2), lot_acres 0.4 (+1.00)"
	if got := alert.GetString("body"); got != want {
		t.Errorf("body = %q\nwant   %q", got, want)
	}
}
