package match

import (
	"encoding/json"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

// listingRecord builds an in-memory listing the way the pipeline stores one.
func listingRecord(t *testing.T, attrs map[string]any) *core.Record {
	t.Helper()
	col := core.NewBaseCollection("listings")
	col.Fields.Add(
		&core.TextField{Name: "title"},
		&core.TextField{Name: "external_id"},
		&core.NumberField{Name: "price"},
		&core.JSONField{Name: "attrs", MaxSize: 1 << 20},
	)
	rec := core.NewRecord(col)
	rec.Set("title", "2755 N Edgewood Dr Provo, UT")
	rec.Set("external_id", "2181743")
	rec.Set("price", 409999)
	rec.Set("attrs", attrs)
	return rec
}

func TestMatchesAttrsFieldPaths(t *testing.T) {
	rec := listingRecord(t, map[string]any{
		"beds":   3,
		"sqft":   2018,
		"status": "ACTIVE",
		"address": map[string]any{
			"city": "Provo",
		},
	})

	cases := []struct {
		name string
		cond Condition
		want bool
	}{
		// The matcher paths straight into attrs, but "attrs." reads naturally
		// and the UI used to suggest fields that way, so both forms resolve.
		{"bare attr", Condition{Field: "sqft", Op: ">=", Value: 2000}, true},
		{"prefixed attr", Condition{Field: "attrs.sqft", Op: ">=", Value: 2000}, true},
		{"bare attr below threshold", Condition{Field: "sqft", Op: ">=", Value: 5000}, false},
		{"prefixed attr below threshold", Condition{Field: "attrs.sqft", Op: ">=", Value: 5000}, false},
		{"nested path", Condition{Field: "address.city", Op: "=", Value: "Provo"}, true},
		{"nested path prefixed", Condition{Field: "attrs.address.city", Op: "=", Value: "Provo"}, true},
		{"typed column wins over attrs", Condition{Field: "price", Op: "<", Value: 500000}, true},
		{"string attr contains", Condition{Field: "status", Op: "contains", Value: "ACT"}, true},
		// A missing field fails its own condition instead of erroring, and
		// "attrs" is not itself addressable as a field.
		{"missing attr", Condition{Field: "garage", Op: "=", Value: "2"}, false},
		{"missing nested attr", Condition{Field: "attrs.nonexistent.deep", Op: "=", Value: "x"}, false},
		{"attrs alone", Condition{Field: "attrs", Op: "=", Value: "anything"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches([]Condition{tc.cond}, rec); got != tc.want {
				t.Errorf("Matches(%+v) = %v, want %v", tc.cond, got, tc.want)
			}
		})
	}
}

func TestMatchesConditionsAreAnded(t *testing.T) {
	rec := listingRecord(t, map[string]any{"beds": 3, "sqft": 2018})

	all := []Condition{
		{Field: "sqft", Op: ">=", Value: 2000},
		{Field: "attrs.beds", Op: ">=", Value: 3},
	}
	if !Matches(all, rec) {
		t.Error("both conditions hold, want match")
	}

	one := []Condition{
		{Field: "sqft", Op: ">=", Value: 2000},
		{Field: "beds", Op: ">=", Value: 4},
	}
	if Matches(one, rec) {
		t.Error("second condition fails, want no match")
	}

	// No conditions matches everything from the seeker's sources.
	if !Matches(nil, rec) {
		t.Error("empty conditions, want match")
	}

	// A malformed row must not silently pass.
	if Matches([]Condition{{Field: "", Op: "=", Value: "x"}}, rec) {
		t.Error("condition with no field, want no match")
	}
}

// The live "Lehi home" seeker, decoded from JSON exactly as it is stored,
// against detail-enriched URE attrs. Each variant breaks one condition, so
// every filter (including the boolean hoa = false) is shown to exclude.
func TestMatchesEnrichedHomeSeeker(t *testing.T) {
	var conds []Condition
	if err := json.Unmarshal([]byte(`[
		{"field":"city","op":"=","value":"Lehi"},
		{"field":"status","op":"in","value":["ACTIVE","COMING SOON"]},
		{"field":"price","op":">","value":0},
		{"field":"price","op":"<=","value":750000},
		{"field":"beds","op":">=","value":4},
		{"field":"sqft","op":">","value":2800},
		{"field":"garage_capacity","op":">=","value":3},
		{"field":"lot_acres","op":">=","value":0.15},
		{"field":"hoa","op":"=","value":false}]`), &conds); err != nil {
		t.Fatal(err)
	}
	if err := Validate(conds); err != nil {
		t.Fatal(err)
	}
	base := func() map[string]any {
		return map[string]any{"city": "Lehi", "status": "ACTIVE", "beds": 4, "sqft": 3100,
			"garage_capacity": 3, "lot_acres": 0.21, "hoa": false}
	}
	if rec := listingRecord(t, base()); !Matches(conds, rec) {
		t.Fatal("qualifying Lehi listing did not match")
	}
	breaks := map[string]func(map[string]any){
		"other city":     func(a map[string]any) { a["city"] = "Orem" },
		"under contract": func(a map[string]any) { a["status"] = "UNDER CONTRACT" },
		"3 beds":         func(a map[string]any) { a["beds"] = 3 },
		"2800 sqft":      func(a map[string]any) { a["sqft"] = 2800 },
		"2-car":          func(a map[string]any) { a["garage_capacity"] = 2 },
		"no garage line": func(a map[string]any) { delete(a, "garage_capacity") },
		"0.14 acres":     func(a map[string]any) { a["lot_acres"] = 0.14 },
		"has HOA":        func(a map[string]any) { a["hoa"] = true },
		"not enriched":   func(a map[string]any) { delete(a, "hoa") },
	}
	for name, mutate := range breaks {
		a := base()
		mutate(a)
		if Matches(conds, listingRecord(t, a)) {
			t.Errorf("%s: matched, want excluded", name)
		}
	}
}

// The soft "Lehi home" seeker as stored: hard city/status/HOA, soft price,
// garage (weight 2), lot, sqft and beds.
const softSeekerJSON = `[
	{"field":"city","op":"=","value":"Lehi"},
	{"field":"status","op":"in","value":["ACTIVE","COMING SOON"]},
	{"field":"hoa","op":"=","value":false},
	{"field":"price","op":">","value":0},
	{"field":"price","op":"<=","value":800000,"soft":true,"floor":850000,"best":650000},
	{"field":"garage_capacity","op":">=","value":3,"soft":true,"floor":2,"best":4,"weight":2},
	{"field":"lot_acres","op":">=","value":0.15,"soft":true,"floor":0.12,"best":0.4},
	{"field":"sqft","op":">","value":2800,"soft":true,"floor":2500,"best":3800},
	{"field":"beds","op":">=","value":4,"soft":true,"floor":3,"best":6}]`

// lehiHome builds a listing record with the given price and attrs over a
// qualifying Lehi baseline.
func lehiHome(t *testing.T, price float64, attrs map[string]any) *core.Record {
	t.Helper()
	a := map[string]any{"city": "Lehi", "status": "ACTIVE", "hoa": false}
	for k, v := range attrs {
		if v == nil {
			delete(a, k)
		} else {
			a[k] = v
		}
	}
	rec := listingRecord(t, a)
	rec.Set("price", price)
	return rec
}

// Real Lehi listings from 2026-10-17, with the scores worked out by hand
// when the user chose these settings.
func TestEvaluateSoftSeeker(t *testing.T) {
	var conds []Condition
	if err := json.Unmarshal([]byte(softSeekerJSON), &conds); err != nil {
		t.Fatal(err)
	}
	if err := Validate(conds); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		price   float64
		attrs   map[string]any
		matched bool
		score   float64 // checked when matched or a plain score miss
	}{
		// 3-car, everything at or above target.
		{"1419 S 270 E", 799999, map[string]any{"garage_capacity": 3, "lot_acres": 0.18, "sqft": 3757, "beds": 5}, true, 1.58},
		// 2-car (-1 x2) paid for by a big lot and space.
		{"425 E 2600 N", 770000, map[string]any{"garage_capacity": 2, "lot_acres": 0.43, "sqft": 3752, "beds": 6}, true, 1.15},
		// 2-car with less to make up for it.
		{"273 W 400 S", 790000, map[string]any{"garage_capacity": 2, "lot_acres": 0.18, "sqft": 3340, "beds": 5}, false, -0.77},
		// Unknown garage scores -1 x2, never a pass.
		{"1371 W 950 S #7", 799900, map[string]any{"garage_capacity": nil, "lot_acres": 0.36, "sqft": 3517, "beds": 4}, false, -0.44},
		// Past a floor fails outright whatever the rest scores.
		{"799 S 1180 W (price past floor)", 959000, map[string]any{"garage_capacity": 3, "lot_acres": 0.48, "sqft": 4087, "beds": 7}, false, 0},
		{"650 N 420 Ln (0-car garage)", 575000, map[string]any{"garage_capacity": 0, "lot_acres": 0.23, "sqft": 3000, "beds": 6}, false, 0},
		{"2 beds", 600000, map[string]any{"garage_capacity": 4, "lot_acres": 0.5, "sqft": 4000, "beds": 2}, false, 0},
		// Hard conditions still gate everything.
		{"HOA", 650000, map[string]any{"hoa": true, "garage_capacity": 4, "lot_acres": 0.5, "sqft": 4000, "beds": 6}, false, 0},
	}
	for _, c := range cases {
		res := Evaluate(conds, lehiHome(t, c.price, c.attrs))
		if res.Matched != c.matched {
			t.Errorf("%s: matched = %v (score %+.2f, %s), want %v", c.name, res.Matched, res.Score, res.Explain(), c.matched)
			continue
		}
		if c.score != 0 && (res.Score < c.score-0.01 || res.Score > c.score+0.01) {
			t.Errorf("%s: score = %+.3f, want %+.2f (%s)", c.name, res.Score, c.score, res.Explain())
		}
	}
}

func TestSoftScoreShape(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	c := Condition{Field: "lot_acres", Op: ">=", Value: 0.15, Soft: true, Floor: f(0.12), Best: f(0.4)}
	for _, tc := range []struct {
		x     any
		score float64
		pass  bool
	}{
		{0.15, 0, true},    // at target
		{0.275, 0.5, true}, // halfway to best
		{0.4, 1, true},     // at best
		{2.0, 1, true},     // capped
		{0.135, -0.5, true},
		{0.12, -1, true}, // at floor: worst acceptable
		{0.119, 0, false},
		{"0.30", 0.6, true}, // numeric strings, as the web UI stores values
		{"n/a", -1, true},   // non-numeric scores like missing
	} {
		score, pass := softScore(c, tc.x, true)
		if pass != tc.pass || (pass && (score < tc.score-1e-9 || score > tc.score+1e-9)) {
			t.Errorf("x=%v: score %v pass %v, want %v %v", tc.x, score, pass, tc.score, tc.pass)
		}
	}
	// Lower-is-better runs the same scale the other way.
	p := Condition{Field: "price", Op: "<=", Value: 800000, Soft: true, Floor: f(850000), Best: f(650000)}
	if s, ok := softScore(p, 725000.0, true); !ok || s != 0.5 {
		t.Errorf("price 725k: %v %v, want 0.5", s, ok)
	}
	if s, ok := softScore(p, 825000.0, true); !ok || s != -0.5 {
		t.Errorf("price 825k: %v %v, want -0.5", s, ok)
	}
}

func TestValidateSoft(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	bad := map[string]Condition{
		"equality op":   {Field: "beds", Op: "=", Value: 4, Soft: true, Floor: f(3), Best: f(6)},
		"no floor":      {Field: "beds", Op: ">=", Value: 4, Soft: true, Best: f(6)},
		"text target":   {Field: "beds", Op: ">=", Value: "four", Soft: true, Floor: f(3), Best: f(6)},
		"floor better":  {Field: "beds", Op: ">=", Value: 4, Soft: true, Floor: f(5), Best: f(6)},
		"best worse":    {Field: "price", Op: "<=", Value: 800000, Soft: true, Floor: f(850000), Best: f(900000)},
		"neg weight":    {Field: "beds", Op: ">=", Value: 4, Soft: true, Floor: f(3), Best: f(6), Weight: -1},
		"floor==target": {Field: "beds", Op: ">=", Value: 4, Soft: true, Floor: f(4), Best: f(6)},
	}
	for name, c := range bad {
		if err := Validate([]Condition{c}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Condition{Field: "price", Op: "<=", Value: "800000", Soft: true, Floor: f(850000), Best: f(650000), Weight: 2}
	if err := Validate([]Condition{ok}); err != nil {
		t.Errorf("valid soft condition rejected: %v", err)
	}
}
