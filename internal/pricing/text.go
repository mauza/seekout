package pricing

import (
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	reJSONLD   = regexp.MustCompile(`(?is)<script[^>]*application/ld\+json[^>]*>(.*?)</script>`)
	reDropTags = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|head)\b.*?</(script|style|noscript|svg|template|head)>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace    = regexp.MustCompile(`\s+`)
	reNumber   = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)
)

// pageText is the readable text of an HTML page, preceded by its JSON-LD
// blocks: shops often render prices client-side, but put them in the
// structured data for search engines.
func pageText(page string, max int) string {
	var b strings.Builder
	for _, m := range reJSONLD.FindAllStringSubmatch(page, 5) {
		ld := collapse(m[1])
		if len(ld) > 3000 {
			ld = ld[:3000]
		}
		b.WriteString(ld)
		b.WriteString("\n")
	}
	body := reDropTags.ReplaceAllString(page, " ")
	body = reTag.ReplaceAllString(body, " ")
	b.WriteString(collapse(html.UnescapeString(body)))
	s := b.String()
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func collapse(s string) string { return strings.TrimSpace(reSpace.ReplaceAllString(s, " ")) }

// foldText normalizes text for the quote check: case, whitespace and
// entities don't count.
func foldText(s string) string {
	return strings.ToLower(collapse(html.UnescapeString(s)))
}

// quoteHolds is the guard against invented prices: the quote must appear
// in the source text, and must itself contain the price.
func quoteHolds(source, quote string, price float64) bool {
	q := foldText(quote)
	if len(q) < 3 || !strings.Contains(foldText(source), q) {
		return false
	}
	for _, m := range reNumber.FindAllString(q, -1) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
		if err != nil {
			continue
		}
		if math.Abs(n-price) <= math.Max(0.01, price*0.005) {
			return true
		}
	}
	return false
}
