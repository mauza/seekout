package pricing

import (
	"math"
	"slices"
)

// Anchor is a web-research reference price for an item, per unit.
// Low..High is the band a used price normally falls in; New is the typical
// new retail price (0 when research found none).
type Anchor struct {
	ID      string
	Low     float64
	Typical float64
	High    float64
	New     float64
	N       int // verified prices it was computed from
	// Used: the band came from used prices. Otherwise it was derived from
	// new retail, which only says what a used price shouldn't exceed.
	Used bool
}

// Typical is the estimated typical unit price and how it was reached.
type Typical struct {
	Price      float64 `json:"typical"`
	Basis      string  `json:"basis"` // comps+anchor | anchor | comps
	Comps      int     `json:"comps"`
	CompsMed   float64 `json:"comps_median,omitempty"`
	Confidence string  `json:"confidence"` // high | medium | low
}

// Minimum comps for each use: with an anchor, the comps median is trusted
// (clamped to the anchor's band) from MinCompsAnchored; without one, from
// MinCompsAlone, and below that only at low confidence.
const (
	MinCompsAnchored = 3
	MinCompsAlone    = 5
	// AgreeWithin: comps median and anchor within this fraction of each
	// other is high confidence.
	AgreeWithin = 0.25
)

// Estimate combines local comps (unit prices of other recent listings of
// the item) with the web anchor. With a used-price anchor, a local market
// that is all overpriced reposts, or a single lowball, can't move typical
// outside the anchor's band, and the anchor alone stands in when there are
// few comps. An anchor derived from new retail only caps typical: it says
// little about used prices (0.8 x new put used electric trucks at $64k
// against a $46k market), so alone it is low confidence. Typical is never
// above the new price: nothing at or over new retail is a deal.
func Estimate(comps []float64, a *Anchor) (Typical, bool) {
	t := Typical{Comps: len(comps)}
	med := 0.0
	if len(comps) > 0 {
		med = median(comps)
		t.CompsMed = med
	}
	switch {
	case a != nil && len(comps) >= MinCompsAnchored:
		t.Price, t.Basis, t.Confidence = med, "comps+anchor", "medium"
		if a.Used {
			t.Price = clamp(med, a.Low, a.High)
			if a.Typical > 0 && math.Abs(med-a.Typical)/a.Typical <= AgreeWithin {
				t.Confidence = "high"
			}
		}
	case a != nil:
		t.Price, t.Basis, t.Confidence = a.Typical, "anchor", "low"
		if a.Used && a.N >= 3 {
			t.Confidence = "medium"
		}
	case len(comps) >= MinCompsAlone:
		t.Price, t.Basis, t.Confidence = med, "comps", "medium"
	case len(comps) > 0:
		t.Price, t.Basis, t.Confidence = med, "comps", "low"
	default:
		return Typical{}, false
	}
	if a != nil && a.New > 0 && t.Price > a.New {
		t.Price = a.New
	}
	return t, t.Price > 0
}

// PctBelow is how far price is under typical, in percent (negative when
// above), rounded to one decimal.
func PctBelow(price, typical float64) float64 {
	if typical <= 0 {
		return 0
	}
	return math.Round((typical-price)/typical*1000) / 10
}

func clamp(x, lo, hi float64) float64 {
	if lo > 0 && x < lo {
		return lo
	}
	if hi > 0 && x > hi {
		return hi
	}
	return x
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// quantile is the q-th quantile (0..1) by linear interpolation.
func quantile(xs []float64, q float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}
