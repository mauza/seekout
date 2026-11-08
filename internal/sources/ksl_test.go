package sources

import (
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/ksl_search.html is a live kayak search trimmed to three results
// and the pageInfo, with the RSC payload split across two push() calls as
// the site streams it. Result 2 was edited to featured with no image, result
// 3 to a null price with an OBO modifier.
func TestKslParseSearch(t *testing.T) {
	page, err := os.ReadFile("testdata/ksl_search.html")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kslParseSearch(string(page), "https://classifieds.ksl.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3", len(got))
	}

	l := got[0]
	if l.ExternalID != "82378065" || l.Price != 30 ||
		l.URL != "https://classifieds.ksl.com/listing/82378065" ||
		!strings.HasPrefix(l.Title, "XGeek Kayak Roof Rack") ||
		!strings.HasPrefix(l.Image, "https://image.ksldigital.com/") ||
		!l.Published.Equal(time.Unix(1792876151, 0)) {
		t.Errorf("listing 0 = %+v", l)
	}
	want := map[string]any{
		"city": "Clearfield", "state": "UT", "zip": "84015",
		"category": "Outdoors and Sporting", "sub_category": "Camping & Outdoors Equipment",
		"seller_type": "Private", "market_type": "Sale", "listing_type": "CLASSIFIED",
		"featured": false, "verified_seller": true,
	}
	for k, v := range want {
		if l.Attrs[k] != v {
			t.Errorf("attr %s = %v, want %v", k, l.Attrs[k], v)
		}
	}
	if _, ok := l.Attrs["price_modifier"]; ok {
		t.Errorf("empty price_modifier stored: %v", l.Attrs)
	}

	if got[1].Attrs["featured"] != true || got[1].Image != "" {
		t.Errorf("featured/no-image listing = %+v", got[1])
	}
	if got[2].Price != 0 || got[2].Attrs["price_modifier"] != "OBO" {
		t.Errorf("null-price listing = %+v", got[2])
	}
}

func rscPage(rsc string) string {
	return `<script>self.__next_f.push([1,"` + strings.ReplaceAll(rsc, `"`, `\"`) + `"])</script>`
}

func TestKslParseSearchEmptyAndBroken(t *testing.T) {
	empty := rscPage(`4e:{"initialState":{"results":[[]],"pageInfo":[{"hasNextPage":false,"total":0}]}}`)
	if got, err := kslParseSearch(empty, "https://classifieds.ksl.com"); err != nil || len(got) != 0 {
		t.Errorf("empty search = %v, %v; want no listings, no error", got, err)
	}

	for name, page := range map[string]string{
		"no payload":       `<html><body>Access denied</body></html>`,
		"total but none":   rscPage(`4e:{"initialState":{"results":[[]],"pageInfo":[{"total":12}]}}`),
		"reshaped results": rscPage(`4e:{"initialState":{"results":{"items":[]}}}`),
		"missing id":       rscPage(`4e:{"initialState":{"results":[[{"title":"x"}]]}}`),
	} {
		if _, err := kslParseSearch(page, "https://classifieds.ksl.com"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestKslCheckURL(t *testing.T) {
	for _, u := range []string{
		"https://classifieds.ksl.com/search/keyword/kayak",
		"https://classifieds.ksl.com/search/cat/Outdoors+and+Sporting/keyword/kayak/priceTo/500",
		"https://cars.ksl.com/search/fuel/Electric/body/Truck/priceTo/50000",
	} {
		if err := kslCheckURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{
		"http://classifieds.ksl.com/search/keyword/kayak",
		"https://classifieds.ksl.com/listing/82378065",
		"https://cars.ksl.com/listing/10864596",
		"https://homes.ksl.com/search",
		"https://classifieds.ksl.com.evil.example/search",
		"",
	} {
		if err := kslCheckURL(u); err == nil {
			t.Errorf("%s: want an error", u)
		}
	}
}

// testdata/ksl_cars_search.html is a live cars.ksl.com electric-truck search
// trimmed to a featured result, two regular ones, and the featured one again
// (the site repeats featured ads further down the page).
func TestKslParseCarsSearch(t *testing.T) {
	page, err := os.ReadFile("testdata/ksl_cars_search.html")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kslParseSearch(string(page), "https://cars.ksl.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 (featured repeat dropped)", len(got))
	}
	l := got[1]
	if l.ExternalID != "10874168" || l.Price != 39749 || l.Title != "2023 Ford F-150 Lightning Lariat" ||
		l.URL != "https://cars.ksl.com/listing/10874168" {
		t.Errorf("listing 1 = %+v", l)
	}
	want := map[string]any{
		"make": "Ford", "model": "F-150 Lightning", "trim": "Lariat", "year": 2023,
		"mileage": float64(26288), "new_used": "Used", "vin": "1FT6W1EVXPWG43109",
		"seller_type": "Dealership", "listing_type": "CAR", "city": "Draper", "featured": false,
	}
	for k, v := range want {
		if l.Attrs[k] != v {
			t.Errorf("attr %s = %v (%T), want %v", k, l.Attrs[k], l.Attrs[k], v)
		}
	}
	if got[0].Attrs["featured"] != true {
		t.Errorf("first listing should be the featured one: %+v", got[0])
	}
}
