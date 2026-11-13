// Package pricing tracks what things sell for, so a seeker can ask for a
// good deal on top of its other conditions.
//
//	new listing a seeker cares about → Classify (LLM) → price_observations
//	  → Estimate (local comps, anchored by web research) → listing attrs.deal
//	daily cron → research items not researched for ResearchEvery → price_refs
//
// Nothing here is specific to one kind of item: the classifier files each
// listing under an item (a price-comparison group) priced per unit, e.g.
// "DDR5 desktop memory (UDIMM)" per GB, so a 16 GB stick and a 64 GB kit
// pool into the same comps. A seeker asks for a deal with ordinary
// conditions on attrs.deal (deal.pct_below >= 15, deal.confidence in
// high,medium); a listing nobody could price has no attrs.deal and fails
// them.
package pricing

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Status values in listing attrs.pricing.status.
const (
	StatusOK        = "ok"         // priced: observations exist
	StatusBundle    = "bundle"     // a system or bundle the sought item is part of
	StatusOffTarget = "off_target" // not what the searches are for
	StatusUnclear   = "unclear"    // can't tell what or how much
	StatusNoPrice   = "no_price"   // listing has no price
	StatusPending   = "pending"    // classification failed; retried
	StatusFailed    = "failed"     // gave up after MaxAttempts
)

// MaxAttempts bounds classification retries of one listing.
const MaxAttempts = 3

// Service holds the pricing configuration. A nil *Service means pricing is
// off (no LLM configured); callers check for nil.
type Service struct {
	LLM           *LLM
	ClassifyModel string
	ResearchModel string
	SearxURL      string // research is off when empty
	ResearchEvery time.Duration
	ResearchMax   int // items researched per run
	CompsWindow   time.Duration
	AnchorMaxAge  time.Duration
	Fetch         *http.Client // result-page fetches; must refuse internal addresses
	Now           func() time.Time
}

// FromEnv builds the service from SEEKOUT_LLM_* / SEEKOUT_SEARXNG_URL, or
// returns nil when no LLM is configured.
func FromEnv(fetch *http.Client) *Service {
	url, key := os.Getenv("SEEKOUT_LLM_URL"), strings.TrimSpace(os.Getenv("SEEKOUT_LLM_KEY"))
	if url == "" || key == "" {
		return nil
	}
	return &Service{
		LLM:           &LLM{URL: url, Key: key, HTTP: &http.Client{Timeout: 4 * time.Minute}},
		ClassifyModel: envOr("SEEKOUT_CLASSIFY_MODEL", "gpt-6-luna"),
		ResearchModel: envOr("SEEKOUT_RESEARCH_MODEL", "gpt-6.1-sol"),
		SearxURL:      os.Getenv("SEEKOUT_SEARXNG_URL"),
		ResearchEvery: time.Duration(envInt("SEEKOUT_RESEARCH_EVERY_DAYS", 4)) * 24 * time.Hour,
		ResearchMax:   envInt("SEEKOUT_RESEARCH_MAX", 20),
		CompsWindow:   30 * 24 * time.Hour,
		AnchorMaxAge:  10 * 24 * time.Hour,
		Fetch:         fetch,
		Now:           time.Now,
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// IsDealField reports whether a condition field reads attrs.deal: those
// conditions can only be decided after pricing, so the pre-match that
// decides whether to price a listing leaves them out.
func IsDealField(field string) bool {
	f := strings.TrimPrefix(field, "attrs.")
	return f == "deal" || strings.HasPrefix(f, "deal.")
}

const dbTime = "2006-01-02 15:04:05.000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(dbTime) }
