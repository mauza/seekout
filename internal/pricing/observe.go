package pricing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// SuspectBelow: a listing more than this many percent under typical is
// marked suspect and low confidence, so default deal filters skip it.
const SuspectBelow = 50

// maxCandidates bounds the existing items shown to the classifier.
const maxCandidates = 60

// terminal statuses are final answers a repost of the same ad can reuse.
var terminal = []string{StatusOK, StatusBundle, StatusOffTarget, StatusUnclear}

// Observe prices one stored listing for the seekers that want it: it files
// the listing under items (reusing the answer for a repost of the same ad,
// else asking the classifier), records the observations, and writes
// attrs.pricing and attrs.deal onto rec (saved). On a classifier failure
// rec is saved with status pending and the error returned; RetryPending
// picks it up later.
func (s *Service) Observe(ctx context.Context, app core.App, rec *core.Record, seekers []*core.Record) error {
	pricing := map[string]any{"at": fmtTime(s.now())}
	prev := pricingOf(rec)
	attempts := 1
	if n, ok := prev["attempts"].(float64); ok {
		attempts = int(n) + 1
	}

	status, reason, err := s.file(ctx, app, rec, seekers)
	if err != nil {
		pricing["status"], pricing["error"], pricing["attempts"] = StatusPending, err.Error(), attempts
		if attempts >= MaxAttempts {
			pricing["status"] = StatusFailed
		}
		setAttr(rec, "pricing", pricing)
		if serr := app.Save(rec); serr != nil {
			return errors.Join(err, serr)
		}
		return err
	}
	pricing["status"] = status
	if reason != "" {
		pricing["reason"] = reason
	}
	setAttr(rec, "pricing", pricing)
	if status == StatusOK {
		if deal, err := s.Deal(app, rec, false); err == nil && deal != nil {
			setAttr(rec, "deal", deal)
		} else if err != nil {
			app.Logger().Warn("pricing: deal estimate", "listing", rec.Id, "error", err)
		}
	}
	return app.Save(rec)
}

// file classifies rec (or copies a repost's answer) and stores its
// observations.
func (s *Service) file(ctx context.Context, app core.App, rec *core.Record, seekers []*core.Record) (string, string, error) {
	if rec.GetFloat("price") <= 0 {
		return StatusNoPrice, "", nil
	}
	seekerIDs := make([]string, 0, len(seekers))
	for _, sk := range seekers {
		seekerIDs = append(seekerIDs, sk.Id)
	}
	// Already filed (a retry after a later save failed): don't double up.
	if existing, _ := app.FindRecordsByFilter("price_observations", "listing = {:l}", "", 1, 0, dbx.Params{"l": rec.Id}); len(existing) > 0 {
		return StatusOK, "", nil
	}

	if status, reason, ok, err := s.copyRepost(app, rec, seekerIDs); err != nil || ok {
		return status, reason, err
	}

	cands, err := candidates(app, rec.GetString("kind"), seekerIDs)
	if err != nil {
		return "", "", err
	}
	status, reason, lines, err := s.classify(ctx, rec, seekers, cands)
	if err != nil {
		return "", "", err
	}
	if status != StatusOK {
		return status, reason, nil
	}
	for i := range lines {
		item, err := resolveItem(app, rec.GetString("kind"), lines[i])
		if err != nil {
			return "", "", fmt.Errorf("item: %w", err)
		}
		lines[i].item = item
	}
	return StatusOK, reason, saveObservations(app, rec, seekerIDs, lines)
}

// copyRepost reuses the filing of an earlier listing (never a later one, so
// a backfill --redo copies only what it already refiled) with the same title
// and price: dealers repost the same ad daily under new ids, and asking the
// model again would only cost tokens (and might file it differently).
func (s *Service) copyRepost(app core.App, rec *core.Record, seekerIDs []string) (string, string, bool, error) {
	since := fmtTime(s.now().Add(-60 * 24 * time.Hour))
	others, err := app.FindRecordsByFilter("listings",
		"title = {:t} && price = {:p} && id != {:id} && created >= {:since} && created <= {:at}", "-created", 10, 0,
		dbx.Params{"t": rec.GetString("title"), "p": rec.GetFloat("price"), "id": rec.Id, "since": since, "at": rec.GetString("created")})
	if err != nil {
		return "", "", false, err
	}
	for _, o := range others {
		p := pricingOf(o)
		st, _ := p["status"].(string)
		if !slices.Contains(terminal, st) {
			continue
		}
		reason, _ := p["reason"].(string)
		if st != StatusOK {
			return st, reason, true, nil
		}
		obs, err := app.FindRecordsByFilter("price_observations", "listing = {:l}", "", 0, 0, dbx.Params{"l": o.Id})
		if err != nil {
			return "", "", false, err
		}
		if len(obs) == 0 {
			continue
		}
		lines := make([]line, 0, len(obs))
		for _, ob := range obs {
			item, err := app.FindRecordById("items", ob.GetString("item"))
			if err != nil {
				continue
			}
			lines = append(lines, line{
				item: item, label: ob.GetString("label"), quantity: ob.GetFloat("quantity"),
				amount: ob.GetFloat("amount"), total: ob.GetFloat("total_price"),
				unitPrice: ob.GetFloat("unit_price"), condition: ob.GetString("condition"),
			})
		}
		if len(lines) == 0 {
			continue
		}
		return StatusOK, "same ad as listing " + o.Id, true, saveObservations(app, rec, seekerIDs, lines)
	}
	return "", "", false, nil
}

// candidates are the existing items shown to the classifier: first those
// these seekers' listings were filed under, then the most recently seen of
// the same listing kind. Items of other kinds stay out: shown GPUs and
// memory, the model called an electric truck "not a tracked product".
func candidates(app core.App, kind string, seekerIDs []string) ([]candidateItem, error) {
	seen := map[string]bool{}
	var out []candidateItem
	add := func(it *core.Record) {
		if seen[it.Id] || len(out) >= maxCandidates {
			return
		}
		seen[it.Id] = true
		out = append(out, candidateItem{
			ID: it.Id, Name: it.GetString("name"), Unit: it.GetString("unit"),
			Category: it.GetString("category"), Description: it.GetString("description"),
		})
	}
	for _, sid := range seekerIDs {
		obs, err := app.FindRecordsByFilter("price_observations", "seekers ~ {:s}", "-observed_at", 300, 0, dbx.Params{"s": sid})
		if err != nil {
			return nil, err
		}
		for _, ob := range obs {
			if seen[ob.GetString("item")] {
				continue
			}
			if it, err := app.FindRecordById("items", ob.GetString("item")); err == nil {
				add(it)
			}
		}
	}
	recent, err := app.FindRecordsByFilter("items", "kind = {:k}", "-last_observed", maxCandidates, 0, dbx.Params{"k": kind})
	if err != nil {
		return nil, err
	}
	for _, it := range recent {
		add(it)
	}
	return out, nil
}

// resolveItem returns the line's existing item, or finds-or-creates the
// proposed one by its folded name. A same-named item with a different unit
// is a different item: the amounts wouldn't be comparable.
func resolveItem(app core.App, kind string, ln line) (*core.Record, error) {
	if ln.itemID != "" {
		return app.FindRecordById("items", ln.itemID)
	}
	name := ln.newItem.Name
	key := normKey(name)
	if it, err := app.FindFirstRecordByFilter("items", "name_key = {:k}", dbx.Params{"k": key}); err == nil {
		if it.GetString("unit") == ln.newItem.Unit {
			return it, nil
		}
		name = fmt.Sprintf("%s (per %s)", name, ln.newItem.Unit)
		key = normKey(name)
		if it, err := app.FindFirstRecordByFilter("items", "name_key = {:k}", dbx.Params{"k": key}); err == nil {
			return it, nil
		}
	}
	col, err := app.FindCollectionByNameOrId("items")
	if err != nil {
		return nil, err
	}
	it := core.NewRecord(col)
	it.Set("name", name)
	it.Set("name_key", key)
	it.Set("unit", ln.newItem.Unit)
	it.Set("kind", kind)
	it.Set("category", ln.newItem.Category)
	it.Set("description", ln.newItem.Description)
	if err := app.Save(it); err != nil {
		// Lost a race with a concurrent poll creating the same item.
		if again, ferr := app.FindFirstRecordByFilter("items", "name_key = {:k}", dbx.Params{"k": key}); ferr == nil {
			return again, nil
		}
		return nil, err
	}
	return it, nil
}

func saveObservations(app core.App, rec *core.Record, seekerIDs []string, lines []line) error {
	col, err := app.FindCollectionByNameOrId("price_observations")
	if err != nil {
		return err
	}
	at := rec.GetDateTime("created").Time()
	if at.IsZero() {
		at = time.Now()
	}
	return app.RunInTransaction(func(tx core.App) error {
		for _, ln := range lines {
			ob := core.NewRecord(col)
			ob.Set("listing", rec.Id)
			ob.Set("item", ln.item.Id)
			ob.Set("seekers", seekerIDs)
			ob.Set("label", ln.label)
			ob.Set("quantity", ln.quantity)
			ob.Set("amount", ln.amount)
			ob.Set("total_price", ln.total)
			ob.Set("unit_price", ln.unitPrice)
			ob.Set("condition", ln.condition)
			ob.Set("dedupe_key", dedupeKey(ln.item.Id, rec.GetString("title"), ln.total))
			ob.Set("observed_at", fmtTime(at))
			if err := tx.Save(ob); err != nil {
				return err
			}
			item, err := tx.FindRecordById("items", ln.item.Id)
			if err != nil {
				return err
			}
			if item.GetDateTime("last_observed").Time().Before(at) {
				item.Set("last_observed", fmtTime(at))
				if err := tx.Save(item); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Deal estimates the typical unit price for each of rec's observations and
// returns the attrs.deal summary of the best one (nil when none could be
// estimated). Comps are other listings of the item seen in the CompsWindow
// before rec, one per ad (reposts collapse on dedupe_key). The anchor is
// the newest research result from AnchorMaxAge before rec; anyAnchor takes
// the newest one regardless (repricing old listings).
func (s *Service) Deal(app core.App, rec *core.Record, anyAnchor bool) (map[string]any, error) {
	obs, err := app.FindRecordsByFilter("price_observations", "listing = {:l}", "", 0, 0, dbx.Params{"l": rec.Id})
	if err != nil {
		return nil, err
	}
	var best map[string]any
	bestPct := -1e9
	for _, ob := range obs {
		at := ob.GetDateTime("observed_at").Time()
		comps, err := s.comps(app, ob, at)
		if err != nil {
			return nil, err
		}
		anchor, err := s.anchor(app, ob.GetString("item"), at, anyAnchor)
		if err != nil {
			return nil, err
		}
		t, ok := Estimate(comps, anchor)
		if !ok {
			continue
		}
		price := ob.GetFloat("unit_price")
		pct := PctBelow(price, t.Price)
		if pct <= bestPct {
			continue
		}
		item, err := app.FindRecordById("items", ob.GetString("item"))
		if err != nil {
			continue
		}
		bestPct = pct
		best = map[string]any{
			"item":        item.Id,
			"item_name":   item.GetString("name"),
			"unit":        item.GetString("unit"),
			"label":       ob.GetString("label"),
			"quantity":    ob.GetFloat("quantity"),
			"amount":      ob.GetFloat("amount"),
			"unit_price":  roundPrice(price),
			"typical":     roundPrice(t.Price),
			"pct_below":   pct,
			"basis":       t.Basis,
			"comps":       t.Comps,
			"confidence":  t.Confidence,
			"computed_at": fmtTime(s.now()),
		}
		// Real deals this far under are rare; a misfiled part or a misread
		// amount is not. Keep the number, but not as a confident deal.
		if pct > SuspectBelow {
			best["confidence"], best["suspect"] = "low", true
		}
		if t.Comps > 0 {
			best["comps_median"] = roundPrice(t.CompsMed)
		}
		if anchor != nil {
			best["anchor"] = anchor.ID
			best["anchor_typical"] = roundPrice(anchor.Typical)
			if anchor.New > 0 {
				best["anchor_new"] = roundPrice(anchor.New)
			}
		}
	}
	return best, nil
}

func (s *Service) comps(app core.App, ob *core.Record, at time.Time) ([]float64, error) {
	recs, err := app.FindRecordsByFilter("price_observations",
		"item = {:i} && observed_at >= {:from} && observed_at <= {:to} && listing != {:l}", "-observed_at", 1000, 0,
		dbx.Params{"i": ob.GetString("item"), "from": fmtTime(at.Add(-s.CompsWindow)), "to": fmtTime(at), "l": ob.GetString("listing")})
	if err != nil {
		return nil, err
	}
	own := ob.GetString("dedupe_key")
	seen := map[string]bool{own: true}
	var out []float64
	for _, r := range recs {
		k := r.GetString("dedupe_key")
		if seen[k] {
			continue
		}
		seen[k] = true
		if p := r.GetFloat("unit_price"); p > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Service) anchor(app core.App, itemID string, at time.Time, anyAge bool) (*Anchor, error) {
	filter, params := "item = {:i} && status = 'ok'", dbx.Params{"i": itemID}
	if !anyAge {
		filter += " && created <= {:to} && created >= {:from}"
		params["to"], params["from"] = fmtTime(at), fmtTime(at.Add(-s.AnchorMaxAge))
	}
	recs, err := app.FindRecordsByFilter("price_refs", filter, "-created", 1, 0, params)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	r := recs[0]
	return &Anchor{
		ID: r.Id, Low: r.GetFloat("low"), Typical: r.GetFloat("typical"), High: r.GetFloat("high"),
		New: r.GetFloat("new_typical"), N: r.GetInt("n_new") + r.GetInt("n_used"), Used: r.GetInt("n_used") >= 2,
	}, nil
}

// Reprice recomputes attrs.deal for an already-filed listing (after new
// research or more comps). It never calls the model.
func (s *Service) Reprice(app core.App, rec *core.Record, anyAnchor bool) error {
	deal, err := s.Deal(app, rec, anyAnchor)
	if err != nil {
		return err
	}
	if deal == nil {
		return nil
	}
	setAttr(rec, "deal", deal)
	return app.Save(rec)
}

func pricingOf(rec *core.Record) map[string]any {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)
	p, _ := attrs["pricing"].(map[string]any)
	if p == nil {
		p = map[string]any{}
	}
	return p
}

// Status is rec's attrs.pricing.status ("" when never priced).
func Status(rec *core.Record) string {
	st, _ := pricingOf(rec)["status"].(string)
	return st
}

func setAttr(rec *core.Record, key string, v any) {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs[key] = v
	rec.Set("attrs", attrs)
}

// roundPrice keeps cents for prices of $100 and up and four decimals below,
// where per-unit prices like $9.38/GB live.
func roundPrice(x float64) float64 {
	if x >= 100 {
		return float64(int64(x*100+0.5)) / 100
	}
	return float64(int64(x*10000+0.5)) / 10000
}

// Unfile removes rec's observations and pricing attrs, so the next Observe
// classifies it from scratch. An item left with no observations goes too
// (with its research): it only existed because of a filing now undone.
func Unfile(app core.App, rec *core.Record) error {
	obs, err := app.FindRecordsByFilter("price_observations", "listing = {:l}", "", 0, 0, dbx.Params{"l": rec.Id})
	if err != nil {
		return err
	}
	items := map[string]bool{}
	for _, o := range obs {
		items[o.GetString("item")] = true
		if err := app.Delete(o); err != nil {
			return err
		}
	}
	for id := range items {
		left, err := app.FindRecordsByFilter("price_observations", "item = {:i}", "", 1, 0, dbx.Params{"i": id})
		if err != nil {
			return err
		}
		if len(left) > 0 {
			continue
		}
		if it, err := app.FindRecordById("items", id); err == nil {
			if err := app.Delete(it); err != nil {
				return err
			}
		}
	}
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)
	delete(attrs, "pricing")
	delete(attrs, "deal")
	rec.Set("attrs", attrs)
	return app.Save(rec)
}
