package pricing

import (
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// ItemSummary is an item with where its price stands now: the comps over
// the CompsWindow, the latest research and the estimate they give.
type ItemSummary struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Unit           string   `json:"unit"`
	Category       string   `json:"category,omitempty"`
	Description    string   `json:"description,omitempty"`
	LastObserved   string   `json:"last_observed,omitempty"`
	LastResearched string   `json:"last_researched,omitempty"`
	Comps          int      `json:"comps"`
	CompsMedian    float64  `json:"comps_median,omitempty"`
	Anchor         *RefView `json:"anchor,omitempty"`
	Typical        *Typical `json:"estimate,omitempty"`
}

// RefView is a price_refs record for display.
type RefView struct {
	ID        string     `json:"id"`
	Created   string     `json:"created"`
	Status    string     `json:"status"`
	Low       float64    `json:"low,omitempty"`
	Typical   float64    `json:"typical,omitempty"`
	High      float64    `json:"high,omitempty"`
	New       float64    `json:"new,omitempty"`
	NNew      int        `json:"n_new"`
	NUsed     int        `json:"n_used"`
	Rejected  int        `json:"rejected"`
	Citations []Citation `json:"citations,omitempty"`
	Queries   []string   `json:"queries,omitempty"`
	Error     string     `json:"error,omitempty"`
	Model     string     `json:"model,omitempty"`
}

// ObservationView is one priced listing line for display.
type ObservationView struct {
	Listing    string  `json:"listing"`
	Title      string  `json:"title"`
	URL        string  `json:"url,omitempty"`
	Label      string  `json:"label,omitempty"`
	Quantity   float64 `json:"quantity"`
	Amount     float64 `json:"amount"`
	TotalPrice float64 `json:"total_price"`
	UnitPrice  float64 `json:"unit_price"`
	Condition  string  `json:"condition"`
	ObservedAt string  `json:"observed_at"`
	Repost     bool    `json:"repost,omitempty"` // same ad as a newer observation; not a separate comp
}

// ItemDetail is an item's summary plus its observations and research.
type ItemDetail struct {
	ItemSummary
	Observations []ObservationView `json:"observations"`
	Refs         []RefView         `json:"refs"`
}

// Items lists items seen in the last `days` days, most recently seen first.
// It needs no LLM, so it works with pricing switched off.
func Items(app core.App, days int, window time.Duration) ([]ItemSummary, error) {
	if days <= 0 {
		days = 90
	}
	recs, err := app.FindRecordsByFilter("items", "last_observed >= {:since}", "-last_observed", 500, 0,
		dbx.Params{"since": fmtTime(time.Now().AddDate(0, 0, -days))})
	if err != nil {
		return nil, err
	}
	out := make([]ItemSummary, 0, len(recs))
	for _, r := range recs {
		s, err := summarize(app, r, window)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Item is one item's detail: observations from the last `days` days and
// its last 10 research results.
func Item(app core.App, id string, days int, window time.Duration) (ItemDetail, error) {
	r, err := app.FindRecordById("items", id)
	if err != nil {
		return ItemDetail{}, err
	}
	if days <= 0 {
		days = 90
	}
	sum, err := summarize(app, r, window)
	if err != nil {
		return ItemDetail{}, err
	}
	d := ItemDetail{ItemSummary: sum, Observations: []ObservationView{}, Refs: []RefView{}}
	obs, err := app.FindRecordsByFilter("price_observations", "item = {:i} && observed_at >= {:since}", "-observed_at", 500, 0,
		dbx.Params{"i": id, "since": fmtTime(time.Now().AddDate(0, 0, -days))})
	if err != nil {
		return d, err
	}
	seen := map[string]bool{}
	for _, o := range obs {
		v := ObservationView{
			Listing: o.GetString("listing"), Label: o.GetString("label"), Quantity: o.GetFloat("quantity"),
			Amount: o.GetFloat("amount"), TotalPrice: o.GetFloat("total_price"), UnitPrice: o.GetFloat("unit_price"),
			Condition: o.GetString("condition"), ObservedAt: o.GetString("observed_at"),
		}
		k := o.GetString("dedupe_key")
		v.Repost, seen[k] = seen[k], true
		if l, err := app.FindRecordById("listings", v.Listing); err == nil {
			v.Title, v.URL = l.GetString("title"), l.GetString("url")
		}
		d.Observations = append(d.Observations, v)
	}
	refs, err := app.FindRecordsByFilter("price_refs", "item = {:i}", "-created", 10, 0, dbx.Params{"i": id})
	if err != nil {
		return d, err
	}
	for _, ref := range refs {
		d.Refs = append(d.Refs, refView(ref, true))
	}
	return d, nil
}

func summarize(app core.App, r *core.Record, window time.Duration) (ItemSummary, error) {
	s := ItemSummary{
		ID: r.Id, Name: r.GetString("name"), Unit: r.GetString("unit"), Category: r.GetString("category"),
		Description: r.GetString("description"), LastObserved: r.GetString("last_observed"),
		LastResearched: r.GetString("last_researched"),
	}
	svc := &Service{CompsWindow: window}
	now := time.Now()
	// Comps as of now: every distinct ad in the window.
	recs, err := app.FindRecordsByFilter("price_observations", "item = {:i} && observed_at >= {:from}", "-observed_at", 1000, 0,
		dbx.Params{"i": r.Id, "from": fmtTime(now.Add(-window))})
	if err != nil {
		return s, err
	}
	seen := map[string]bool{}
	var comps []float64
	for _, o := range recs {
		k := o.GetString("dedupe_key")
		if seen[k] {
			continue
		}
		seen[k] = true
		comps = append(comps, o.GetFloat("unit_price"))
	}
	s.Comps = len(comps)
	if len(comps) > 0 {
		s.CompsMedian = roundPrice(median(comps))
	}
	refs, err := app.FindRecordsByFilter("price_refs", "item = {:i}", "-created", 1, 0, dbx.Params{"i": r.Id})
	if err != nil {
		return s, err
	}
	if len(refs) > 0 {
		v := refView(refs[0], false)
		s.Anchor = &v
	}
	a, err := svc.anchor(app, r.Id, now, true)
	if err != nil {
		return s, err
	}
	if t, ok := Estimate(comps, a); ok {
		t.Price, t.CompsMed = roundPrice(t.Price), roundPrice(t.CompsMed)
		s.Typical = &t
	}
	return s, nil
}

func refView(r *core.Record, detail bool) RefView {
	v := RefView{
		ID: r.Id, Created: r.GetString("created"), Status: r.GetString("status"),
		Low: r.GetFloat("low"), Typical: r.GetFloat("typical"), High: r.GetFloat("high"), New: r.GetFloat("new_typical"),
		NNew: r.GetInt("n_new"), NUsed: r.GetInt("n_used"), Rejected: r.GetInt("rejected"),
		Error: r.GetString("error"), Model: r.GetString("model"),
	}
	if detail {
		_ = r.UnmarshalJSONField("citations", &v.Citations)
		_ = r.UnmarshalJSONField("queries", &v.Queries)
	}
	return v
}

// DealLine renders rec's attrs.deal for alert text, e.g. "Deal: $9.38/GB vs
// typical $11.20/GB, 16% under (comps+anchor, 7 comps, high)"; "" without one.
func DealLine(rec *core.Record) string {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)
	d, _ := attrs["deal"].(map[string]any)
	if d == nil {
		return ""
	}
	unit, _ := d["unit"].(string)
	price, _ := d["unit_price"].(float64)
	typical, _ := d["typical"].(float64)
	pct, _ := d["pct_below"].(float64)
	basis, _ := d["basis"].(string)
	conf, _ := d["confidence"].(string)
	comps, _ := d["comps"].(float64)
	dir := "under"
	if pct < 0 {
		dir, pct = "over", -pct
	}
	return fmt.Sprintf("Deal: %s vs typical %s, %.0f%% %s (%s, %.0f comps, %s)",
		PerUnit(price, unit), PerUnit(typical, unit), pct, dir, basis, comps, conf)
}

// PerUnit formats a unit price: "$9.38/GB", "$650 each".
func PerUnit(p float64, unit string) string {
	s := fmt.Sprintf("$%.2f", p)
	if p >= 100 {
		s = fmt.Sprintf("$%.0f", p)
	}
	if unit == "" || unit == "each" {
		return s + " each"
	}
	return s + "/" + unit
}
