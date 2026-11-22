package sources

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/listing"
)

// testdata/fbm_search.html is a live saltlakecity search trimmed to two real
// items, a duplicate of the first item (verifying dedupe), and a third item
// using custom_title, Free price, no photo, and is_pending=true.
func TestFbmParseSearch(t *testing.T) {
	page, err := os.ReadFile("testdata/fbm_search.html")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fbmParseSearch(string(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 (duplicate dropped)", len(got))
	}

	l := got[0]
	if l.ExternalID != "1536052261682419" || l.Price != 1124 ||
		l.URL != "https://www.facebook.com/marketplace/item/1536052261682419/" ||
		!strings.Contains(l.Title, "RTX 4080") ||
		!strings.HasPrefix(l.Image, "https://") ||
		!l.Published.Equal(time.Unix(1795293016, 0).UTC()) {
		t.Errorf("listing 0 = %+v", l)
	}
	want := map[string]any{
		"city": "Ogden", "state": "UT",
		"seller_name": "Lowell Gundersen",
		"category_id": "1792291877663080",
		"sold": false, "pending": false, "live": true,
	}
	for k, v := range want {
		if l.Attrs[k] != v {
			t.Errorf("attr %s = %v (%T), want %v", k, l.Attrs[k], l.Attrs[k], v)
		}
	}
	if d, ok := l.Attrs["delivery_types"].([]string); !ok || len(d) != 2 || d[0] != "IN_PERSON" {
		t.Errorf("delivery_types = %#v", l.Attrs["delivery_types"])
	}

	// Third item: custom_title fallback, Free price (0), no photo, pending=true.
	l2 := got[2]
	if l2.ExternalID != "999000111222" || l2.Title != "Free Broken GPU Parts" ||
		l2.Price != 0 || l2.Image != "" || l2.Attrs["pending"] != true {
		t.Errorf("listing 2 = %+v", l2)
	}
}

func TestFbmParseCategoryFeed(t *testing.T) {
	page := `<html><body><script type="application/json">{"data":{"viewer":{"marketplace_feed_stories":{"edges":[{"node":{"__typename":"MarketplaceFeedListingStory","listing":{"__typename":"GroupCommerceProductItem","id":"2131361350802290","marketplace_listing_title":"2023 Rivian R1T","listing_price":{"amount":"48500.00","formatted_amount":"$48,500"},"creation_time":1795367486,"is_live":true,"location_text":{"text":"Lehi, UT"}}}}]}}}}</script></body></html>`
	got, err := fbmParseSearch(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	l := got[0]
	if l.ExternalID != "2131361350802290" || l.Price != 48500 || l.Attrs["city"] != "Lehi" || l.Attrs["state"] != "UT" {
		t.Errorf("category listing = %+v", l)
	}
}

func TestFbmParseSearchEmptyAndBroken(t *testing.T) {
	emptyNoResults := `<html><body><script type="application/json">{"feed_units":{"edges":[{"node":{"__typename":"MarketplaceSearchFeedNoResults"}}]}}</script></body></html>`
	if got, err := fbmParseSearch(emptyNoResults); err != nil || len(got) != 0 {
		t.Errorf("no-results sentinel = %v, %v; want empty, nil", got, err)
	}
	emptyEdges := `<html><body><script type="application/json">{"feed_units":{"edges":[]}}</script></body></html>`
	if got, err := fbmParseSearch(emptyEdges); err != nil || len(got) != 0 {
		t.Errorf("empty edges = %v, %v; want empty, nil", got, err)
	}

	for name, page := range map[string]string{
		"login wall":    `<html><body><form id="login_form"></form></body></html>`,
		"missing id":    `<script type="application/json">{"feed_units":{"edges":[{"node":{"listing":{"marketplace_listing_title":"RTX 3090"}}}]}}</script>`,
		"missing title": `<script type="application/json">{"feed_units":{"edges":[{"node":{"listing":{"id":"123"}}}]}}</script>`,
	} {
		if _, err := fbmParseSearch(page); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestFbmParseDetail(t *testing.T) {
	page, err := os.ReadFile("testdata/fbm_item.html")
	if err != nil {
		t.Fatal(err)
	}
	l := listing.Listing{ExternalID: "1536052261682419", URL: "https://www.facebook.com/marketplace/item/1536052261682419/"}
	if err := fbmParseDetail(string(page), &l); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.Description, "30 day warranty included") {
		t.Errorf("description = %q", l.Description)
	}
	if l.Attrs["condition"] != "Used - like new" || l.Attrs["brand"] != "Gigabyte" {
		t.Errorf("attrs = %+v", l.Attrs)
	}
}

func TestFbmParseVehicleDetail(t *testing.T) {
	page, err := os.ReadFile("testdata/fbm_vehicle_item.html")
	if err != nil {
		t.Fatal(err)
	}
	l := listing.Listing{ExternalID: "30274853525438123", URL: "https://www.facebook.com/marketplace/item/30274853525438123/"}
	if err := fbmParseDetail(string(page), &l); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.Description, "Quad-Motor Large Pack") {
		t.Errorf("description = %q", l.Description)
	}
	want := map[string]any{
		"make":           "Rivian",
		"model":          "r1s launch edition",
		"mileage":        float64(35590),
		"fuel_type":      "ELECTRIC",
		"transmission":   "AUTOMATIC",
		"exterior_color": "green",
		"interior_color": "black",
		"condition":      "VERY_GOOD",
		"seller_type":    "PRIVATE_SELLER",
	}
	for k, v := range want {
		if l.Attrs[k] != v {
			t.Errorf("attr %s = %v (%T), want %v", k, l.Attrs[k], l.Attrs[k], v)
		}
	}
}

func TestFbmParseDetailUnavailableAndBroken(t *testing.T) {
	l := listing.Listing{ExternalID: "1234567890", URL: "https://www.facebook.com/marketplace/item/1234567890/"}
	if err := fbmParseDetail(`<html><body>This content isn't available right now</body></html>`, &l); err != nil {
		t.Fatalf("unavailable listing should not error: %v", err)
	}
	if l.Attrs["unavailable"] != true {
		t.Errorf("attrs = %+v, want unavailable=true", l.Attrs)
	}

	l2 := listing.Listing{ExternalID: "1234567890", URL: "https://www.facebook.com/marketplace/item/1234567890/"}
	if err := fbmParseDetail(`<html><body><form id="login_form"></form></body></html>`, &l2); err == nil {
		t.Errorf("blocked detail page: want error")
	}

	// Mismatched item ID in payload must error instead of mixing another item's data.
	page, err := os.ReadFile("testdata/fbm_item.html")
	if err != nil {
		t.Fatal(err)
	}
	l3 := listing.Listing{ExternalID: "9999999999", URL: "https://www.facebook.com/marketplace/item/9999999999/"}
	if err := fbmParseDetail(string(page), &l3); err == nil {
		t.Errorf("mismatched item id: want error")
	}
}

func TestFbmCheckURL(t *testing.T) {
	for _, u := range []string{
		"https://www.facebook.com/marketplace/saltlakecity/search/?query=ddr5&daysSinceListed=7",
		"https://facebook.com/marketplace/lehi/search/?query=rtx+3090",
		"https://www.facebook.com/marketplace/saltlakecity/vehicles?maxPrice=50000",
		"https://www.facebook.com/marketplace/",
	} {
		if err := fbmCheckURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{
		"http://www.facebook.com/marketplace/saltlakecity/search/?query=ddr5",
		"https://www.facebook.com/marketplace/item/1536052261682419/",
		"https://www.facebook.com/marketplace/item/1536052261682419",
		"https://www.facebook.com/groups/12345",
		"https://www.facebook.com.evil.example/marketplace/saltlakecity/search/",
		"",
	} {
		if err := fbmCheckURL(u); err == nil {
			t.Errorf("%s: want an error", u)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFbmEnrichToggleAndLoginRedirect(t *testing.T) {
	origTransport := fbmTransport
	origDelay := fbmEnrichDelay
	t.Cleanup(func() {
		fbmTransport = origTransport
		fbmEnrichDelay = origDelay
	})
	fbmEnrichDelay = 0

	itemHTML, err := os.ReadFile("testdata/fbm_item.html")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fbmTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(r.URL.RawQuery, "redirect_login=1") {
			r.URL.Path = "/login/"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(itemHTML))),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})

	col := core.NewBaseCollection("sources")
	col.Fields.Add(&core.JSONField{Name: "config"})

	// enrich=off makes zero HTTP calls.
	srcOff := core.NewRecord(col)
	srcOff.Set("config", map[string]any{"url": "https://www.facebook.com/marketplace/saltlakecity/search/?query=ddr5", "enrich": "off"})
	lOff := listing.Listing{ExternalID: "1536052261682419", URL: "https://www.facebook.com/marketplace/item/1536052261682419/"}
	if err := (fbmSource{}).Enrich(context.Background(), srcOff, &lOff); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || lOff.Description != "" {
		t.Errorf("enrich=off: calls=%d desc=%q, want 0 and empty", calls, lOff.Description)
	}

	// enrich=on fetches the detail page and fills description.
	srcOn := core.NewRecord(col)
	srcOn.Set("config", map[string]any{"url": "https://www.facebook.com/marketplace/saltlakecity/search/?query=ddr5", "enrich": "on"})
	lOn := listing.Listing{ExternalID: "1536052261682419", URL: "https://www.facebook.com/marketplace/item/1536052261682419/"}
	if err := (fbmSource{}).Enrich(context.Background(), srcOn, &lOn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(lOn.Description, "30 day warranty included") {
		t.Errorf("enrich=on: calls=%d desc=%q", calls, lOn.Description)
	}

	// Redirect to /login/ fails loudly.
	if _, err := fbmFetch(context.Background(), "https://www.facebook.com/marketplace/saltlakecity/search/?redirect_login=1"); err == nil || !strings.Contains(err.Error(), "/login/") {
		t.Errorf("login redirect err = %v, want /login/ error", err)
	}
}
