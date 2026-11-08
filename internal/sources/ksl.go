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

// kslSource polls a KSL Classifieds (classifieds.ksl.com) or KSL Cars
// (cars.ksl.com) search. Both are the same Next.js app and share the
// result shape; car results add make, model, year, mileage and so on.
//
// The site is a Next.js app with no public API. A search page such as
// /search/cat/Outdoors+and+Sporting/keyword/kayak/priceTo/500 is
// server-rendered, and its React Server Components payload (the
// self.__next_f.push([1,"..."]) script strings) carries the first page of
// results as JSON under "initialState":{"results":[[...]]}. The filters live
// in the URL path, so the user builds the search on the site and pastes it.
//
// Only that first page is reachable without a browser: /page/N is ignored,
// and infinite scroll goes through a Next server action whose id changes on
// every KSL deploy. The page holds about 11 classifieds or 19 cars, a few
// of them featured (sorted by their own rules, and repeated further down),
// the rest newest first by display time. A narrow search polled every 10-30
// minutes misses nothing; a broad one that gets more new posts per interval
// than the page holds will.
//
// The site sits behind PerimeterX, which answers Go's default User-Agent
// with a 403 captcha page; a browser User-Agent is let through.
type kslSource struct{}

type kslConfig struct {
	URL string `json:"url"`
}

var kslTransport = http.DefaultTransport.(*http.Transport).Clone()

var kslPush = regexp.MustCompile(`self\.__next_f\.push\(\[1,"((?:[^"\\]|\\.)*)"\]\)`)

const (
	kslResultsKey = `"initialState":{"results":`
	kslTotalKey   = `"pageInfo":[{`
)

// kslItem is the part of a search result the source reads.
type kslItem struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	Price        *float64 `json:"price"`
	PriceMod     string   `json:"priceModifier"`
	RentalUnit   string   `json:"rentalPriceUnitRate"`
	CreatedAt    int64    `json:"createdAt"`
	ListingType  string   `json:"listingType"`
	Category     string   `json:"category"`
	SubCategory  string   `json:"subCategory"`
	SellerType   string   `json:"sellerType"`
	MarketType   string   `json:"marketType"`
	IsFeatured   bool     `json:"isFeatured"`
	MemberVerify bool     `json:"memberIsVerified"`
	NoImage      bool     `json:"noImage"`
	// Cars only.
	Make         string   `json:"make"`
	Model        string   `json:"model"`
	Trim         string   `json:"trim"`
	Year         *int     `json:"makeYear"`
	Mileage      *float64 `json:"mileage"`
	NewUsed      string   `json:"newUsed"`
	VIN          string   `json:"vin"`
	PrimaryImage *struct {
		URL string `json:"url"`
	} `json:"primaryImage"`
	Location *struct {
		City  string `json:"city"`
		State string `json:"state"`
		Zip   string `json:"zip"`
	} `json:"location"`
}

func (kslSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg kslConfig
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, fmt.Errorf("ksl: bad config: %w", err)
	}
	if err := kslCheckURL(cfg.URL); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := (&http.Client{Transport: kslTransport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("ksl: fetch: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("ksl: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if strings.Contains(string(body), "px-cloud.net") || strings.Contains(string(body), "px-captcha") {
			return nil, fmt.Errorf("ksl: status %d: blocked by the site's bot check (PerimeterX)", resp.StatusCode)
		}
		return nil, fmt.Errorf("ksl: status %d", resp.StatusCode)
	}
	return kslParseSearch(string(body), "https://"+req.URL.Host)
}

// kslCheckURL requires an https search URL on classifieds.ksl.com or
// cars.ksl.com, so a typo'd or pasted listing URL errors instead of polling
// as "nothing new".
func kslCheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Host != "classifieds.ksl.com" && u.Host != "cars.ksl.com") ||
		!strings.HasPrefix(u.Path, "/search") {
		return fmt.Errorf("ksl: url must be a https://classifieds.ksl.com/search/... or https://cars.ksl.com/search/... page, got %q", raw)
	}
	return nil
}

// kslParseSearch reads the results out of a search page's RSC payload.
// base is the site origin listing URLs are built on.
func kslParseSearch(page, base string) ([]listing.Listing, error) {
	var payload strings.Builder
	for _, m := range kslPush.FindAllStringSubmatch(page, -1) {
		var s string
		if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &s); err != nil {
			return nil, fmt.Errorf("ksl: undecodable page payload: %w", err)
		}
		payload.WriteString(s)
	}
	rsc := payload.String()

	i := strings.Index(rsc, kslResultsKey)
	if i < 0 {
		return nil, fmt.Errorf("ksl: no search results in page — layout likely changed")
	}
	var pages [][]kslItem
	if err := json.NewDecoder(strings.NewReader(rsc[i+len(kslResultsKey):])).Decode(&pages); err != nil {
		return nil, fmt.Errorf("ksl: unreadable search results — layout likely changed: %w", err)
	}

	var out []listing.Listing
	seen := map[int64]bool{}
	for _, p := range pages {
		for _, it := range p {
			if it.ID == 0 || it.Title == "" {
				return nil, fmt.Errorf("ksl: result without id or title — layout likely changed")
			}
			if seen[it.ID] {
				continue // featured ads repeat further down the page
			}
			seen[it.ID] = true
			out = append(out, kslListing(it, base))
		}
	}
	if len(out) == 0 && kslTotal(rsc) != 0 {
		return nil, fmt.Errorf("ksl: page reports results but lists none — layout likely changed")
	}
	return out, nil
}

// kslTotal is pageInfo.total, or -1 when the page doesn't say.
func kslTotal(rsc string) int {
	i := strings.Index(rsc, kslTotalKey)
	if i < 0 {
		return -1
	}
	j := strings.Index(rsc[i:], `"total":`)
	if j < 0 {
		return -1
	}
	rest := rsc[i+j+len(`"total":`):]
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		return -1
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return -1
	}
	return n
}

func kslListing(it kslItem, base string) listing.Listing {
	id := strconv.FormatInt(it.ID, 10)
	l := listing.Listing{
		ExternalID: id,
		Title:      strings.TrimSpace(it.Title),
		URL:        base + "/listing/" + id,
		Attrs: map[string]any{
			"featured":        it.IsFeatured,
			"verified_seller": it.MemberVerify,
		},
	}
	if it.Price != nil {
		l.Price = *it.Price
	}
	if it.CreatedAt > 0 {
		l.Published = time.Unix(it.CreatedAt, 0).UTC()
	}
	if it.PrimaryImage != nil && !it.NoImage {
		l.Image = it.PrimaryImage.URL
	}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			l.Attrs[k] = v
		}
	}
	if it.Location != nil {
		set("city", it.Location.City)
		set("state", it.Location.State)
		set("zip", it.Location.Zip)
	}
	set("category", it.Category)
	set("sub_category", it.SubCategory)
	set("seller_type", it.SellerType)
	set("market_type", it.MarketType)
	set("listing_type", it.ListingType)
	set("price_modifier", it.PriceMod)
	set("rental_unit", it.RentalUnit)
	set("make", it.Make)
	set("model", it.Model)
	set("trim", it.Trim)
	set("new_used", it.NewUsed)
	set("vin", it.VIN)
	if it.Year != nil {
		l.Attrs["year"] = *it.Year
	}
	if it.Mileage != nil {
		l.Attrs["mileage"] = *it.Mileage
	}
	return l
}

func init() {
	Register(plugin.Info{
		Type:  "ksl",
		Label: "KSL Classifieds / Cars",
		Description: "A classifieds.ksl.com or cars.ksl.com search: set the filters on the site and paste the URL. " +
			"Only the first page (~11 classifieds, ~19 cars) is read, so keep the search narrow or the interval short.",
		Fields: []plugin.Field{
			{Key: "url", Label: "Search URL", Type: plugin.URL, Required: true,
				Hint:        "classifieds.ksl.com or cars.ksl.com search page, sorted newest first (the default)",
				Placeholder: "https://classifieds.ksl.com/search/keyword/kayak/priceTo/500"},
		},
	}, kslSource{})
}
