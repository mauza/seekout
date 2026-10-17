package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"gitlab.mauza.net/mauza/seekout/internal/listing"
)

// ureSource polls utahrealestate.com, the public search over the Utah MLS.
//
// The site is a jQuery app with no public JSON API: GET
// /search/map.search/type/{n} sets the session's default search state
// (all of Utah, that property type, active/coming-soon/backup statuses),
// and POST /search/map.inline.results/pg/{pg}/sort/{sort}/paging/0/dh/0
// then returns one page of results as JSON whose "html" field holds the
// server-rendered listing cards. We poll the newest-first pages and let
// the pipeline's (source, external_id) dedupe surface new listings.
//
// Since 2026-10 every XHR must carry the X-CSRF-TOKEN the search page
// embeds in <meta name="csrf-token">; without it the results POST answers
// 200 with an empty body.
type ureSource struct{}

type ureConfig struct {
	// Type is a listing category: residential (default), multi_unit,
	// land, commercial, farm, rental.
	Type string `json:"type"`
	// Pages is how many 50-result pages to fetch per poll (default 1).
	Pages int `json:"pages"`
}

const ureBase = "https://www.utahrealestate.com"

var ureTypeCodes = map[string]string{
	"residential": "1",
	"multi_unit":  "2",
	"land":        "3",
	"commercial":  "5",
	"farm":        "6",
	"rental":      "7",
}

var (
	ureCardSplit = regexp.MustCompile(`<li id="mls-inline-`)
	ureListno    = regexp.MustCompile(`listno="(\d+)"`)
	ureSlug      = regexp.MustCompile(`address="([^"]+)"`)
	urePrice     = regexp.MustCompile(`list___price">\s*\$?([\d,]+)`)
	ureBedsBaths = regexp.MustCompile(`(\d+) bds &bull; (\d+) ba &bull; ([\d,]+) SqFt`)
	ureStatus    = regexp.MustCompile(`class="[^"]*status-\d+"></i>\s*([A-Za-z_ -]+)`)
	ureDays      = regexp.MustCompile(`Days on URE:\s*([^<]+)`)
	ureAgent     = regexp.MustCompile(`(?s)listing___agent[^>]*>\s*<span>\s*([^<]+?)\s*</span>`)
	ureAddress   = regexp.MustCompile(`(?s)listing___address[^>]*>\s*<span>\s*([^<]+?)\s*</span>`)
	ureOpenHouse = regexp.MustCompile(`(?s)openhouse___label">\s*([^<]+?)\s*</div>`)
	urePhoto     = regexp.MustCompile(`<img src="([^"]+)" class="listing-img"`)
	ureNumStrip  = regexp.MustCompile(`[,$]`)
	ureCSRF      = regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)"`)
	ureGarage    = regexp.MustCompile(`Garage Capacity:\s*(\d+)`)
	ureLotAcres  = regexp.MustCompile(`Lot Size:\s*([\d.]+)\s*Acres`)
	ureOGListno  = regexp.MustCompile(`<meta property="og:url" content="https?://www\.utahrealestate\.com/(\d+)"`)
	ureHOAFee    = regexp.MustCompile(`<ul id="hoa_info">\s*<li>\s*([^<]*?)\s*</li>`)
	ureZip       = regexp.MustCompile(`^\d{5}$`)
	// City words in the address slug are Title-case; street words are
	// UPPERCASE, numbers or single letters ("3311-W-3550-230-West-Haven").
	ureCityWord = regexp.MustCompile(`^[A-Z][a-z][A-Za-z'.]*$`)
)

func (ureSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg ureConfig
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, fmt.Errorf("ure: bad config: %w", err)
	}
	typeName := cfg.Type
	if _, ok := ureTypeCodes[typeName]; !ok {
		typeName = "residential"
	}
	code := ureTypeCodes[typeName]
	pages := cfg.Pages
	if pages <= 0 {
		pages = 1
	}
	if pages > 5 {
		pages = 5
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("ure: cookie jar: %w", err)
	}
	client := &http.Client{
		Timeout:   60 * time.Second,
		Jar:       jar,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}

	// A fresh session per poll: the page GET establishes the default
	// search state, so the poll is stateless and survives cookie expiry.
	csrf, err := ureGet(ctx, client, ureBase+"/search/map.search/type/"+code)
	if err != nil {
		return nil, err
	}

	var out []listing.Listing
	seen := map[string]bool{}
	for pg := 1; pg <= pages; pg++ {
		u := fmt.Sprintf("%s/search/map.inline.results/pg/%d/sort/entry_date_desc/paging/0/dh/0", ureBase, pg)
		var res struct {
			HTML        string `json:"html"`
			ListingData []struct {
				Listno string `json:"listno"`
			} `json:"listing_data"`
		}
		if err := urePost(ctx, client, u, ureBase+"/search/map.search/type/"+code, csrf, &res); err != nil {
			return nil, fmt.Errorf("ure: page %d: %w", pg, err)
		}
		listings, err := ureParseCards(res.HTML, typeName)
		if err != nil {
			return nil, fmt.Errorf("ure: page %d: %w", pg, err)
		}
		if len(res.ListingData) > 0 && len(listings) == 0 {
			return nil, fmt.Errorf("ure: page %d: %d results but no parseable cards — page layout likely changed", pg, len(res.ListingData))
		}
		for _, l := range listings {
			if seen[l.ExternalID] {
				continue
			}
			seen[l.ExternalID] = true
			out = append(out, l)
		}
		if len(res.ListingData) < 50 {
			break // last page
		}
	}
	return out, nil
}

// Enrich reads the listing's detail page for what the result cards lack:
// garage_capacity, lot_acres, hoa and hoa_fee. Listings with an HOA have a
// <ul id="hoa_info"> block and listings without one have no HOA markup at
// all, so hoa=false is only trusted on a page proven to be this listing's
// full detail page; anything else is an error, never a silent "no HOA".
func (ureSource) Enrich(ctx context.Context, _ *core.Record, l *listing.Listing) error {
	if l.URL == "" {
		return fmt.Errorf("ure: no detail URL")
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return err
	}
	ureUA(req)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ure detail: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("ure detail: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ure detail %s: status %d", l.URL, resp.StatusCode)
	}
	// A listing pulled between the results page and this fetch redirects
	// to an off-market report. That is a final answer, not a failure: store
	// it without detail (so it can't match) instead of retrying forever.
	if strings.Contains(resp.Request.URL.Path, "/report/public.offmarket") {
		if l.Attrs == nil {
			l.Attrs = map[string]any{}
		}
		l.Attrs["status"] = "OFF MARKET"
		return nil
	}
	return ureParseDetail(string(body), l)
}

// ureParseDetail adds detail-page facts to l.Attrs.
func ureParseDetail(page string, l *listing.Listing) error {
	// The canonical og:url names the listing the page is really about.
	if m := ureOGListno.FindStringSubmatch(page); m == nil || m[1] != l.ExternalID ||
		!strings.Contains(page, "Property Features") {
		return fmt.Errorf("ure detail %s: not a full detail page for MLS# %s — layout likely changed", l.URL, l.ExternalID)
	}
	if l.Attrs == nil {
		l.Attrs = map[string]any{}
	}
	if m := ureGarage.FindStringSubmatch(page); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			l.Attrs["garage_capacity"] = v
		}
	}
	if m := ureLotAcres.FindStringSubmatch(page); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			l.Attrs["lot_acres"] = v
		}
	}
	m := ureHOAFee.FindStringSubmatch(page)
	l.Attrs["hoa"] = m != nil
	if m != nil && m[1] != "" {
		l.Attrs["hoa_fee"] = normalizeSpace(m[1])
	}
	return nil
}

// ureParseCards turns one page of server-rendered listing cards into
// normalized listings.
func ureParseCards(html string, listingType string) ([]listing.Listing, error) {
	if html == "" {
		return nil, nil
	}
	now := time.Now().UTC()
	var out []listing.Listing
	for _, frag := range ureCardSplit.Split(html, -1) {
		mListno := ureListno.FindStringSubmatch(frag)
		if len(mListno) < 2 || mListno[1] == "" {
			continue
		}
		l := listing.Listing{ExternalID: mListno[1]}

		if m := ureAddress.FindStringSubmatch(frag); m != nil {
			l.Title = normalizeSpace(m[1])
		}
		attrs := map[string]any{"mls": l.ExternalID, "listing_type": listingType}
		if m := ureSlug.FindStringSubmatch(frag); m != nil {
			l.URL = ureBase + "/" + strings.ToLower(m[1]) + "/" + l.ExternalID
			city, zip := ureCityZip(m[1], l.Title)
			if city != "" {
				attrs["city"] = city
			}
			if zip != "" {
				attrs["zip"] = zip
			}
		}
		if m := urePrice.FindStringSubmatch(frag); m != nil {
			if price, err := strconv.ParseFloat(ureNumStrip.ReplaceAllString(m[1], ""), 64); err == nil {
				l.Price = price
			}
		}
		if m := urePhoto.FindStringSubmatch(frag); m != nil {
			l.Image = m[1]
		}
		if m := ureAgent.FindStringSubmatch(frag); m != nil {
			l.Description = normalizeSpace(m[1])
		}
		if m := ureStatus.FindStringSubmatch(frag); m != nil {
			attrs["status"] = normalizeSpace(m[1])
		}
		if m := ureBedsBaths.FindStringSubmatch(frag); m != nil {
			for i, key := range []string{"beds", "baths", "sqft"} {
				// SqFt is thousands-separated ("1,018"), so the separator has
				// to go before parsing or every listing over 999 sqft - which
				// is nearly all of them - silently loses the field.
				if v, err := strconv.Atoi(ureNumStrip.ReplaceAllString(m[i+1], "")); err == nil {
					attrs[key] = v
				}
			}
		}
		days, ok := ureParseDays(maybeMatch(ureDays, frag))
		if ok {
			attrs["days_on_market"] = days
			l.Published = now.AddDate(0, 0, -days)
		}
		if m := ureOpenHouse.FindStringSubmatch(frag); m != nil && m[1] != "" {
			attrs["open_house"] = normalizeSpace(m[1])
		}
		l.Attrs = attrs
		out = append(out, l)
	}
	return out, nil
}

// ureCityZip pulls the city and ZIP out of an address slug such as
// "3311-W-3550-230-West-Haven-UT-84401". The visible address has no
// separator between street and city, so the city is the trailing run of
// Title-case slug words. Ambiguous or inconsistent cases yield no city
// rather than a guess, since the value becomes seeker-matchable.
func ureCityZip(slug, address string) (city, zip string) {
	parts := strings.Split(slug, "-")
	if len(parts) < 3 || parts[len(parts)-2] != "UT" {
		return "", ""
	}
	if ureZip.MatchString(parts[len(parts)-1]) {
		zip = parts[len(parts)-1]
	}
	start := len(parts) - 2
	for start > 0 && ureCityWord.MatchString(parts[start-1]) {
		start--
	}
	// A run reaching back to the house number means the street is
	// Title-case too, so street and city can't be told apart.
	if start <= 1 {
		return "", zip
	}
	if c := strings.Join(parts[start:len(parts)-2], " "); c != "" && strings.HasSuffix(address, " "+c+", UT") {
		city = c
	}
	return city, zip
}

func ureParseDays(s string) (int, bool) {
	s = normalizeSpace(s)
	if s == "" {
		return 0, false
	}
	if s == "Just Listed" {
		return 0, true
	}
	days, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return days, true
}

// ureGet loads the search page, which sets the session's search state, and
// returns the CSRF token it embeds.
func ureGet(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	ureUA(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	m := ureCSRF.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("GET %s: no csrf-token meta tag — page layout likely changed", rawURL)
	}
	return string(m[1]), nil
}

func urePost(ctx context.Context, client *http.Client, rawURL, referer, csrf string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(""))
	if err != nil {
		return err
	}
	ureUA(req)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", referer)
	req.Header.Set("X-CSRF-TOKEN", csrf)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: status %d", rawURL, resp.StatusCode)
	}
	if len(body) == 0 {
		return fmt.Errorf("POST %s: empty response — session or CSRF token rejected", rawURL)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("POST %s: bad JSON: %w", rawURL, err)
	}
	return nil
}

func ureUA(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
}

// maybeMatch returns the first regex match in s as a joined group-1
// slice entry, or "".
func maybeMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil && len(m) > 1 {
		return m[1]
	}
	return ""
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func init() {
	Register("ure", ureSource{})
}
