package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// One page of cards, trimmed from a live map.inline.results response to the
// markup the parser actually reads: a just-listed card with an open house, a
// coming-soon card with no "Days on URE" line, and a card whose sqft has no
// thousands separator.
const ureFixture = `
<li id="mls-inline-2181743" class="result">
  <div class="property___card" listno="2181743" address="2755-N-EDGEWOOD-DR-Provo-UT-84604" tabindex="0">
    <div class="openhouse___label">
        Public          Open House Sat, 11:00AM - 1:00PM
    </div>
    <img src="https://assets.utahrealestate.com/photos/640x480/2181743_6a90.jpg" class="listing-img" alt="MLS# 2181743 Photo" />
    <p><i class="icon icon-circle status-1"></i> ACTIVE  <span style="float: right;">Days on URE: Just Listed</span></p>
    <div class="list___price">$409,999</div>
    3 bds &bull; 2 ba &bull; 2,018 SqFt.
    <div class="listing___address truncate">
        <span>
            2755 N Edgewood Dr Provo, UT
        </span>
    </div>
    <div class="listing___agent truncate">
        <span>
            Candice Smoot | Smoot &amp; Bennett Real Estate LLC
        </span>
    </div>
  </div>
</li>
<li id="mls-inline-2181738" class="result">
  <div class="property___card" listno="2181738" address="5826-WILLOW-WOOD-LN-South-Ogden-UT-84403" tabindex="0">
    <img src="https://assets.utahrealestate.com/photos/640x480/2181738_bb01.jpg" class="listing-img" alt="MLS# 2181738 Photo" />
    <p><i class="icon icon-circle status-9"></i> COMING SOON</p>
    <div class="list___price">$1,250,000</div>
    5 bds &bull; 4 ba &bull; 4,600 SqFt.
    <div class="listing___address truncate"><span>5826 Willow Wood Ln South Ogden, UT</span></div>
    <div class="listing___agent truncate"><span>Jane Agent | Some Brokerage</span></div>
  </div>
</li>
<li id="mls-inline-2180500" class="result">
  <div class="property___card" listno="2180500" address="12-S-MAIN-ST-4B-Salt-Lake-City-UT-84101" tabindex="0">
    <p><i class="icon icon-circle status-2"></i> UNDER CONTRACT  <span style="float: right;">Days on URE: 34</span></p>
    <div class="list___price">$315,000</div>
    2 bds &bull; 1 ba &bull; 720 SqFt.
    <div class="listing___address truncate"><span>12 S Main St #4B Salt Lake City, UT</span></div>
    <div class="listing___agent truncate"><span>Bob Broker | Downtown Realty</span></div>
  </div>
</li>`

func TestUreParseCards(t *testing.T) {
	got, err := ureParseCards(ureFixture, "residential")
	if err != nil {
		t.Fatalf("ureParseCards: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d cards, want 3", len(got))
	}

	first := got[0]
	if first.ExternalID != "2181743" {
		t.Errorf("external id = %q, want 2181743", first.ExternalID)
	}
	if first.Title != "2755 N Edgewood Dr Provo, UT" {
		t.Errorf("title = %q", first.Title)
	}
	// Price and sqft are both thousands-separated in the markup; a bare
	// Atoi/ParseFloat on either silently drops the field.
	if first.Price != 409999 {
		t.Errorf("price = %v, want 409999", first.Price)
	}
	if first.Attrs["sqft"] != 2018 {
		t.Errorf("sqft = %v, want 2018", first.Attrs["sqft"])
	}
	if first.Attrs["beds"] != 3 || first.Attrs["baths"] != 2 {
		t.Errorf("beds/baths = %v/%v, want 3/2", first.Attrs["beds"], first.Attrs["baths"])
	}
	if first.Attrs["status"] != "ACTIVE" {
		t.Errorf("status = %v, want ACTIVE", first.Attrs["status"])
	}
	if first.Attrs["open_house"] != "Public Open House Sat, 11:00AM - 1:00PM" {
		t.Errorf("open_house = %v", first.Attrs["open_house"])
	}
	if first.URL != "https://www.utahrealestate.com/2755-n-edgewood-dr-provo-ut-84604/2181743" {
		t.Errorf("url = %q", first.URL)
	}
	if first.Image == "" {
		t.Error("image not parsed")
	}
	if first.Description != "Candice Smoot | Smoot &amp; Bennett Real Estate LLC" {
		t.Errorf("description = %q", first.Description)
	}
	// "Just Listed" is day zero, so published is today, not the zero time.
	if days := first.Attrs["days_on_market"]; days != 0 {
		t.Errorf("days_on_market = %v, want 0", days)
	}
	if time.Since(first.Published) > time.Hour {
		t.Errorf("published = %v, want ~now for a just-listed card", first.Published)
	}

	// Coming-soon cards carry no "Days on URE" line at all: the field is
	// absent rather than guessed, and published stays unset.
	coming := got[1]
	if _, ok := coming.Attrs["days_on_market"]; ok {
		t.Errorf("days_on_market present for a coming-soon card: %v", coming.Attrs["days_on_market"])
	}
	if !coming.Published.IsZero() {
		t.Errorf("published = %v, want zero", coming.Published)
	}
	if coming.Attrs["sqft"] != 4600 || coming.Price != 1250000 {
		t.Errorf("sqft/price = %v/%v, want 4600/1250000", coming.Attrs["sqft"], coming.Price)
	}
	if _, ok := coming.Attrs["open_house"]; ok {
		t.Error("open_house present on a card without one")
	}

	// City and ZIP come from the address slug; multi-word cities and a
	// unit number ("#4B") between street and city must not confuse it.
	for i, want := range [][2]string{{"Provo", "84604"}, {"South Ogden", "84403"}, {"Salt Lake City", "84101"}} {
		if got[i].Attrs["city"] != want[0] || got[i].Attrs["zip"] != want[1] {
			t.Errorf("card %d city/zip = %v/%v, want %s/%s", i, got[i].Attrs["city"], got[i].Attrs["zip"], want[0], want[1])
		}
	}

	last := got[2]
	if last.Attrs["sqft"] != 720 {
		t.Errorf("unseparated sqft = %v, want 720", last.Attrs["sqft"])
	}
	if last.Attrs["days_on_market"] != 34 {
		t.Errorf("days_on_market = %v, want 34", last.Attrs["days_on_market"])
	}
	if last.Image != "" {
		t.Errorf("image = %q, want empty for a card with no photo", last.Image)
	}
}

func TestUreParseCardsEmpty(t *testing.T) {
	got, err := ureParseCards("", "residential")
	if err != nil || got != nil {
		t.Fatalf("empty html: got %v, %v; want nil, nil", got, err)
	}
	// Markup that no longer matches must yield nothing rather than a
	// half-populated listing; Poll turns this into an explicit error.
	got, err = ureParseCards(`<li id="mls-inline-1"><div class="renamed___card"></div></li>`, "residential")
	if err != nil {
		t.Fatalf("unmatched markup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parsed %d cards from unmatched markup, want 0", len(got))
	}
}

func TestUreCityZip(t *testing.T) {
	cases := []struct{ slug, address, city, zip string }{
		{"3311-W-3550-230-West-Haven-UT-84401", "3311 W 3550 #230 West Haven, UT", "West Haven", "84401"},
		{"702-W-MCKELLAR-ST-Tooele-UT-84074", "702 W Mckellar St Tooele, UT", "Tooele", "84074"},
		{"392-E-700--J-Vernal-UT-84078", "392 E 700 # J Vernal, UT", "Vernal", "84078"},
		{"4089-W-BENVIEW-DR-West-Valley-City-UT-84120", "4089 W Benview Dr West Valley City, UT", "West Valley City", "84120"},
		// A Title-case street can't be split from the city: no guess.
		{"12-Main-St-Provo-UT-84604", "12 Main St Provo, UT", "", "84604"},
		// Slug and visible address disagree: no guess.
		{"12-MAIN-ST-Provo-UT-84604", "12 Main St Orem, UT", "", "84604"},
		{"not-a-utah-slug", "somewhere", "", ""},
	}
	for _, c := range cases {
		city, zip := ureCityZip(c.slug, c.address)
		if city != c.city || zip != c.zip {
			t.Errorf("ureCityZip(%q) = %q, %q; want %q, %q", c.slug, city, zip, c.city, c.zip)
		}
	}
}

// The site answers a results POST without the page's CSRF token with 200
// and an empty body (2026-10); the poll must send it and name the failure.
func TestUreCSRFHandshake(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`<html><head><meta name="csrf-token" content="` + token + `" /></head></html>`))
			return
		}
		if r.Header.Get("X-CSRF-TOKEN") != token {
			return // 200, empty body
		}
		w.Write([]byte(`{"html":"","listing_data":[]}`))
	}))
	defer srv.Close()
	ctx := context.Background()

	got, err := ureGet(ctx, srv.Client(), srv.URL)
	if err != nil || got != token {
		t.Fatalf("ureGet = %q, %v; want the meta token", got, err)
	}
	var res map[string]any
	if err := urePost(ctx, srv.Client(), srv.URL, srv.URL, got, &res); err != nil {
		t.Fatalf("urePost with token: %v", err)
	}
	err = urePost(ctx, srv.Client(), srv.URL, srv.URL, "wrong", &res)
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("urePost without a valid token: err = %v, want an empty-response error", err)
	}
}
