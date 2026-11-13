package pricing

import (
	"strings"
	"testing"
)

func TestEstimate(t *testing.T) {
	anchor := &Anchor{Low: 8, Typical: 10, High: 12, New: 13, N: 4, Used: true}
	fromNew := &Anchor{Low: 7.8, Typical: 10.4, High: 13, New: 13, N: 4}
	cases := []struct {
		name  string
		comps []float64
		a     *Anchor
		want  Typical
		ok    bool
	}{
		{"nothing", nil, nil, Typical{}, false},
		{"comps agree with anchor", []float64{9, 10, 11}, anchor,
			Typical{Price: 10, Basis: "comps+anchor", Comps: 3, CompsMed: 10, Confidence: "high"}, true},
		{"overpriced local market clamped to the band", []float64{15, 16, 17, 18}, anchor,
			Typical{Price: 12, Basis: "comps+anchor", Comps: 4, CompsMed: 16.5, Confidence: "medium"}, true},
		{"few comps: anchor alone", []float64{20, 21}, anchor,
			Typical{Price: 10, Basis: "anchor", Comps: 2, CompsMed: 20.5, Confidence: "medium"}, true},
		{"new-only anchor doesn't lift comps to its band", []float64{6, 7, 8}, fromNew,
			Typical{Price: 7, Basis: "comps+anchor", Comps: 3, CompsMed: 7, Confidence: "medium"}, true},
		{"new-only anchor still caps comps", []float64{14, 15, 16}, fromNew,
			Typical{Price: 13, Basis: "comps+anchor", Comps: 3, CompsMed: 15, Confidence: "medium"}, true},
		{"new-only anchor alone is low confidence", nil, fromNew,
			Typical{Price: 10.4, Basis: "anchor", Confidence: "low"}, true},
		{"thin anchor is low confidence", nil, &Anchor{Low: 8, Typical: 10, High: 12, N: 1, Used: true},
			Typical{Price: 10, Basis: "anchor", Confidence: "low"}, true},
		{"comps alone", []float64{10, 11, 12, 13, 14}, nil,
			Typical{Price: 12, Basis: "comps", Comps: 5, CompsMed: 12, Confidence: "medium"}, true},
		{"too few comps alone", []float64{10, 12}, nil,
			Typical{Price: 11, Basis: "comps", Comps: 2, CompsMed: 11, Confidence: "low"}, true},
		{"never above new retail", []float64{14, 14, 14}, &Anchor{Low: 8, Typical: 14, High: 20, New: 12, N: 3, Used: true},
			Typical{Price: 12, Basis: "comps+anchor", Comps: 3, CompsMed: 14, Confidence: "high"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Estimate(c.comps, c.a)
			if ok != c.ok || got != c.want {
				t.Fatalf("Estimate = %+v, %v; want %+v, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestPctBelow(t *testing.T) {
	if got := PctBelow(8.5, 10); got != 15 {
		t.Fatalf("PctBelow(8.5, 10) = %v", got)
	}
	if got := PctBelow(11, 10); got != -10 {
		t.Fatalf("PctBelow(11, 10) = %v", got)
	}
}

func TestQuoteHolds(t *testing.T) {
	src := `Corsair Vengeance 32GB (2 x 16GB) DDR5-6000 &mdash; <b>Now</b> $1,129.99 at
	Newegg. "price": "89.50"`
	text := pageText(src, 10000)
	cases := []struct {
		quote string
		price float64
		want  bool
	}{
		{"Now $1,129.99 at Newegg", 1129.99, true},
		{"now   $1,129.99 AT newegg", 1129.99, true}, // case and whitespace don't count
		{`"price": "89.50"`, 89.5, true},
		{"Now $1,129.99 at Newegg", 999, false},  // quote doesn't state that price
		{"Now $999.99 at Newegg", 999.99, false}, // not in the source
		{"$1,129", 1129, true},                   // a prefix of the figure is still in the text
		{"Corsair Vengeance 32GB", 32, true},     // states 32, though a capacity: amount checks catch misuse
		{"", 0, false},
	}
	for _, c := range cases {
		if got := quoteHolds(text, c.quote, c.price); got != c.want {
			t.Errorf("quoteHolds(%q, %v) = %v, want %v", c.quote, c.price, got, c.want)
		}
	}
}

func TestPageTextKeepsJSONLD(t *testing.T) {
	page := `<html><head><title>x</title><script type="application/ld+json">{"offers":{"price":"119.99"}}</script></head>
	<body><script>var junk = 1;</script><p>Hello&nbsp;world</p><style>.a{}</style></body></html>`
	got := pageText(page, 10000)
	if !strings.Contains(got, `"price":"119.99"`) || !strings.Contains(got, "Hello") || strings.Contains(got, "junk") {
		t.Fatalf("pageText = %q", got)
	}
}

func TestValidateLines(t *testing.T) {
	cands := []candidateItem{{ID: "mem", Name: "DDR5 desktop memory", Unit: "GB"}}
	one := func(l classifyLine) classifyResult { return classifyResult{Verdict: "items", Lines: []classifyLine{l}} }

	st, lines := validateLines(one(classifyLine{ItemID: "mem", Quantity: 1, Amount: 32, Condition: "used"}), 320, cands)
	if st != StatusOK || len(lines) != 1 || lines[0].unitPrice != 10 || lines[0].itemID != "mem" {
		t.Fatalf("kit: %s %+v", st, lines)
	}
	// Two kits at the ad's total: per-GB price over both.
	st, lines = validateLines(one(classifyLine{ItemID: "mem", Quantity: 2, Amount: 32}), 640, cands)
	if st != StatusOK || lines[0].unitPrice != 10 || lines[0].condition != "unknown" {
		t.Fatalf("two kits: %s %+v", st, lines)
	}
	// Missing amount on a per-GB item can't be priced.
	if st, _ := validateLines(one(classifyLine{ItemID: "mem", Quantity: 1}), 100, cands); st != StatusUnclear {
		t.Fatalf("no amount: %s", st)
	}
	// "each" items default the amount to 1.
	st, lines = validateLines(one(classifyLine{NewItem: newItem{Name: "RTX 4090", Unit: "Each"}, Quantity: 1}), 1500, cands)
	if st != StatusOK || lines[0].unitPrice != 1500 || lines[0].newItem.Unit != "each" {
		t.Fatalf("each: %s %+v", st, lines)
	}
	// Several lines need their own prices.
	two := classifyResult{Verdict: "items", Lines: []classifyLine{
		{ItemID: "mem", Quantity: 1, Amount: 16}, {ItemID: "mem", Quantity: 1, Amount: 8},
	}}
	if st, _ := validateLines(two, 90, cands); st != StatusUnclear {
		t.Fatalf("two lines one price: %s", st)
	}
	two.Lines[0].LinePrice, two.Lines[1].LinePrice = 60, 30
	if st, lines := validateLines(two, 90, cands); st != StatusOK || lines[0].unitPrice != 3.75 || lines[1].unitPrice != 3.75 {
		t.Fatalf("two priced lines: %s %+v", st, lines)
	}
	// An unknown item id without a proposal is not a guess we keep.
	if st, _ := validateLines(one(classifyLine{ItemID: "nope", Quantity: 1, Amount: 16}), 90, cands); st != StatusUnclear {
		t.Fatalf("unknown id: %s", st)
	}
	if st, _ := validateLines(classifyResult{Verdict: "bundle"}, 900, cands); st != StatusBundle {
		t.Fatalf("bundle: %s", st)
	}
}

func TestAnchorBand(t *testing.T) {
	// New prices only: used typical derived from new.
	b, ok := anchorBand([]Citation{
		{UnitPrice: 10, Condition: "new"}, {UnitPrice: 12, Condition: "new"}, {UnitPrice: 200, Condition: "new"},
	})
	if !ok || b.New != 11 || b.Typical != 8.8 || b.Low != 6.6 || b.High != 11 || b.nNew != 2 {
		t.Fatalf("new only: %+v %v", b, ok)
	}
	// Two or more used prices make the band.
	b, ok = anchorBand([]Citation{
		{UnitPrice: 8, Condition: "used"}, {UnitPrice: 9, Condition: "used"}, {UnitPrice: 10, Condition: "used"}, {UnitPrice: 12, Condition: "new"},
	})
	if !ok || b.Typical != 9 || b.New != 12 || b.nUsed != 3 || b.Low > 7.66 || b.High < 10.34 {
		t.Fatalf("used: %+v %v", b, ok)
	}
	if _, ok := anchorBand(nil); ok {
		t.Fatal("empty band ok")
	}
}

func TestIsDealField(t *testing.T) {
	for f, want := range map[string]bool{"deal.pct_below": true, "attrs.deal.confidence": true, "deal": true, "dealer": false, "price": false} {
		if IsDealField(f) != want {
			t.Errorf("IsDealField(%q) != %v", f, want)
		}
	}
}

func TestAmountsStated(t *testing.T) {
	gb := func(amount, qty float64) []line { return []line{{unit: "GB", amount: amount, quantity: qty}} }
	cases := []struct {
		lines []line
		text  string
		want  bool
	}{
		{gb(32, 1), "Corsair Vengeance DDR5 6000MHz 32 GB kit", true},
		{gb(32, 1), "SK Hynix 16GBx2 kit DDR5 4800Mhz", true}, // 16 x 2
		{gb(1, 1), "DDR5 5200 SO-DIMM Laptop memory", false},  // no capacity given
		{gb(128, 1), "Kingston FURY Beast 4x32GB 5600", true},
		{[]line{{unit: "each", amount: 1, quantity: 1}}, "RTX 4090", true},
	}
	for _, c := range cases {
		if got := amountsStated(c.lines, c.text); got != c.want {
			t.Errorf("amountsStated(%v, %q) = %v, want %v", c.lines[0].amount, c.text, got, c.want)
		}
	}
}
