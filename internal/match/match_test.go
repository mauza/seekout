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
