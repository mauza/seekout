package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

const classifySystem = `You file classified ads into price-comparison items for a deal tracker. The tracker learns what each item typically sells for from many ads, and flags ads priced well under that.

You get the searches that want this ad priced, the ad, and the items already tracked (from every search, so most may be unrelated to this ad).

An item is a group of interchangeable products whose price scales with one unit:
- Pick the unit so different sizes pool together: memory and storage per "GB", a GPU or phone model "each", bulk goods per "lb" or "ft". Use "each" when nothing divides.
- Keep products in one item only when their price per unit is normally within about 20% of each other. Split by generation, form factor or tier when that changes the unit price (e.g. "DDR5 desktop memory (UDIMM)" vs "DDR5 laptop memory (SO-DIMM)"; "NVIDIA RTX 4090" vs "NVIDIA RTX 4080"). Don't split by brand, color or capacity when those price alike per unit.
- The ad's text decides what is being sold; sellers often pick the wrong category. Use the category and attrs only to settle what the text leaves open (memory listed under "Desktop Hardware" is desktop memory). Never make a catch-all item such as "X (form factor unspecified)": pick the item the evidence points to, or return unclear.
- Reuse an existing item from the list whenever one fits. Propose new_item only when none does: a generic name (no seller wording, no capacity when priced per capacity), the unit, a short category, and a description saying what belongs in the item and what doesn't.

Verdicts:
- items: the ad sells one or more priceable products. One line per distinct product. quantity = how many pieces are sold (a kit counts as one piece; "two kits" is 2). amount = how many of the item's unit one piece holds (a 2x16GB kit is 32 when the unit is GB; 1 when the unit is each). line_price = the price the ad states for that line, or 0 when only the ad's total price applies (only allowed with a single line). label = the specific product in a few words (e.g. "Corsair Vengeance 2x16GB DDR5-6000 CL30").
- bundle: a complete system or bundle in which the searched-for product is only a component (a prebuilt PC or laptop on a memory search). No lines.
- off_target: parts or accessories for a product rather than the product (a water block, cooler, cable, backplate or box for a GPU is not a GPU), unless a search is for those parts; or not the kind of thing any of the searches is looking for, or a wanted / ISO / trade-only ad. No lines. Judge this by the searches (their names and conditions) only. The existing items are just there to reuse: an ad that fits a search but no existing item gets a new item, never off_target.
- unclear: you can't tell the product, the amount, or which price goes with which line (e.g. "16GB OR 8GB" at one price); or the product is broken, for parts or not working, since that price says nothing about working ones. No lines.

Never guess an amount the ad doesn't state or clearly imply. Set item_id to "" and fill new_item when proposing an item; otherwise set item_id and leave new_item's fields empty.`

var classifyTool = Tool{
	Name:        "file_listing",
	Description: "Record how this ad is filed for price tracking.",
	Parameters: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"verdict", "reason", "lines"},
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"items", "bundle", "off_target", "unclear"}},
			"reason":  map[string]any{"type": "string", "description": "one short sentence"},
			"lines": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"item_id", "new_item", "label", "quantity", "amount", "line_price", "condition"},
					"properties": map[string]any{
						"item_id": map[string]any{"type": "string"},
						"new_item": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"name", "unit", "category", "description"},
							"properties": map[string]any{
								"name":        map[string]any{"type": "string"},
								"unit":        map[string]any{"type": "string"},
								"category":    map[string]any{"type": "string"},
								"description": map[string]any{"type": "string"},
							},
						},
						"label":      map[string]any{"type": "string"},
						"quantity":   map[string]any{"type": "number"},
						"amount":     map[string]any{"type": "number"},
						"line_price": map[string]any{"type": "number"},
						"condition":  map[string]any{"type": "string", "enum": []string{"new", "used", "unknown"}},
					},
				},
			},
		},
	},
}

type newItem struct {
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

type classifyLine struct {
	ItemID    string  `json:"item_id"`
	NewItem   newItem `json:"new_item"`
	Label     string  `json:"label"`
	Quantity  float64 `json:"quantity"`
	Amount    float64 `json:"amount"`
	LinePrice float64 `json:"line_price"`
	Condition string  `json:"condition"`
}

type classifyResult struct {
	Verdict string         `json:"verdict"`
	Reason  string         `json:"reason"`
	Lines   []classifyLine `json:"lines"`
}

// candidateItem is how an existing item is shown to the classifier.
type candidateItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
}

// line is a validated, priced line ready to become an observation.
type line struct {
	item      *core.Record // nil until resolved; then the item record
	itemID    string       // an existing candidate
	newItem   newItem
	unit      string // the item's unit
	label     string
	quantity  float64
	amount    float64
	total     float64
	unitPrice float64
	condition string
}

// classify asks the model how to file rec. It returns the verdict and, for
// "items", the validated lines; anything the rules can't price becomes
// unclear rather than a guess.
func (s *Service) classify(ctx context.Context, rec *core.Record, seekers []*core.Record, cands []candidateItem) (string, string, []line, error) {
	in := map[string]any{
		"searches": searchesFor(seekers),
		"listing":  listingFor(rec),
		"items":    cands,
	}
	user, err := json.MarshalIndent(in, "", " ")
	if err != nil {
		return "", "", nil, err
	}
	var out classifyResult
	if err := s.LLM.Call(ctx, s.ClassifyModel, classifySystem, string(user), classifyTool, &out); err != nil {
		return "", "", nil, err
	}
	verdict, lines := validateLines(out, rec.GetFloat("price"), cands)
	if verdict == StatusOK && !amountsStated(lines, rec.GetString("title")+" "+rec.GetString("description")) {
		return StatusUnclear, "amount not stated in the ad (model said: " + out.Reason + ")", nil, nil
	}
	return verdict, out.Reason, lines, nil
}

// validateLines turns the model's answer into priced lines, or demotes it
// to unclear when a line can't be priced.
func validateLines(out classifyResult, total float64, cands []candidateItem) (string, []line) {
	switch out.Verdict {
	case StatusBundle, StatusOffTarget, StatusUnclear:
		return out.Verdict, nil
	case "items":
	default:
		return StatusUnclear, nil
	}
	known := map[string]candidateItem{}
	for _, c := range cands {
		known[c.ID] = c
	}
	if len(out.Lines) == 0 {
		return StatusUnclear, nil
	}
	var lines []line
	for _, l := range out.Lines {
		ln := line{label: strings.TrimSpace(l.Label), condition: l.Condition}
		unit := ""
		if c, ok := known[l.ItemID]; ok {
			ln.itemID, unit = c.ID, c.Unit
		} else {
			ln.newItem = newItem{
				Name:        strings.TrimSpace(l.NewItem.Name),
				Unit:        normUnit(l.NewItem.Unit),
				Category:    strings.TrimSpace(l.NewItem.Category),
				Description: strings.TrimSpace(l.NewItem.Description),
			}
			if ln.newItem.Name == "" || ln.newItem.Unit == "" {
				return StatusUnclear, nil
			}
			unit = ln.newItem.Unit
		}
		ln.unit = unit
		ln.quantity = math.Round(l.Quantity)
		if ln.quantity < 1 {
			ln.quantity = 1
		}
		ln.amount = l.Amount
		if ln.amount <= 0 {
			if unit != "each" {
				return StatusUnclear, nil
			}
			ln.amount = 1
		}
		switch {
		case l.LinePrice > 0:
			ln.total = l.LinePrice
		case len(out.Lines) == 1:
			ln.total = total
		default:
			return StatusUnclear, nil // several lines, one price
		}
		if ln.total <= 0 {
			return StatusUnclear, nil
		}
		if ln.condition != "new" && ln.condition != "used" {
			ln.condition = "unknown"
		}
		ln.unitPrice = ln.total / (ln.quantity * ln.amount)
		lines = append(lines, ln)
	}
	return StatusOK, lines
}

// amountsStated is the guard against invented sizes: for items priced per
// GB, lb, ..., each line's amount must appear in the ad as a number, or as
// the product of two (a "2x16GB" kit is 32).
func amountsStated(lines []line, text string) bool {
	var nums []float64
	for _, m := range reNumber.FindAllString(text, -1) {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64); err == nil {
			nums = append(nums, n)
		}
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 0.01*math.Max(1, b) }
	for _, ln := range lines {
		if ln.unit == "each" {
			continue
		}
		ok := false
		for i, a := range nums {
			if near(a, ln.amount) {
				ok = true
				break
			}
			for _, b := range nums[i+1:] {
				if near(a*b, ln.amount) {
					ok = true
					break
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func normUnit(u string) string {
	u = strings.TrimSpace(u)
	if strings.EqualFold(u, "each") || strings.EqualFold(u, "ea") || strings.EqualFold(u, "unit") {
		return "each"
	}
	return u
}

// searchesFor describes the seekers the listing pre-matched, so the model
// knows what is being looked for.
func searchesFor(seekers []*core.Record) []map[string]any {
	out := make([]map[string]any, 0, len(seekers))
	for _, sk := range seekers {
		var conds []map[string]any
		_ = sk.UnmarshalJSONField("conditions", &conds)
		keep := conds[:0]
		for _, c := range conds {
			if f, _ := c["field"].(string); !IsDealField(f) {
				keep = append(keep, c)
			}
		}
		out = append(out, map[string]any{"name": sk.GetString("name"), "conditions": keep})
	}
	return out
}

// listingFor is the listing as the classifier sees it: the text, price and
// the plain attrs (not our own pricing bookkeeping).
func listingFor(rec *core.Record) map[string]any {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)
	plain := map[string]any{}
	for k, v := range attrs {
		if k == "deal" || k == "pricing" {
			continue
		}
		switch v.(type) {
		case string, float64, bool, int:
			plain[k] = v
		}
	}
	desc := rec.GetString("description")
	if len(desc) > 2000 {
		desc = desc[:2000] + "…"
	}
	return map[string]any{
		"title":       rec.GetString("title"),
		"price":       rec.GetFloat("price"),
		"description": desc,
		"attrs":       plain,
	}
}

// normKey is an item name folded for the unique index.
func normKey(name string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

func dedupeKey(itemID, title string, total float64) string {
	return fmt.Sprintf("%s|%s|%.2f", itemID, normKey(title), total)
}
