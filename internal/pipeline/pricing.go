package pipeline

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/match"
	"github.com/mauza/seekout/internal/pricing"
)

// Pricing prices new listings for the seekers that want them (see
// internal/pricing). Nil turns pricing off; main sets it from the
// environment.
var Pricing *pricing.Service

// pricingSeekers are the seekers rec would match with their deal and price
// conditions left out: the ones that would want to know its price. Price
// conditions are left out so comps are the whole market, not just what fit
// a budget (a "trucks under $50k" search would otherwise only ever see
// sub-$50k trucks, and call that typical). Homes are not priced.
func pricingSeekers(app core.App, src, rec *core.Record, seekers []*core.Record) []*core.Record {
	if rec.GetString("kind") == "home" {
		return nil
	}
	var out []*core.Record
	for _, sk := range seekers {
		if !seekerApplies(sk, src, rec.GetString("kind")) {
			continue
		}
		conds, ok := seekerConditions(app, sk)
		if !ok {
			continue
		}
		if match.Evaluate(marketConditions(conds), rec).Matched {
			out = append(out, sk)
		}
	}
	return out
}

func seekerConditions(app core.App, sk *core.Record) ([]match.Condition, bool) {
	var conds []match.Condition
	if err := sk.UnmarshalJSONField("conditions", &conds); err != nil {
		app.Logger().Error("seeker conditions", "seeker", sk.Id, "error", err)
		return nil, false
	}
	return conds, true
}

func withoutDeal(conds []match.Condition) []match.Condition {
	out := make([]match.Condition, 0, len(conds))
	for _, c := range conds {
		if !pricing.IsDealField(c.Field) {
			out = append(out, c)
		}
	}
	return out
}

// marketConditions drops deal and price conditions (see pricingSeekers).
func marketConditions(conds []match.Condition) []match.Condition {
	out := make([]match.Condition, 0, len(conds))
	for _, c := range withoutDeal(conds) {
		if c.Field != "price" {
			out = append(out, c)
		}
	}
	return out
}

func hasDeal(conds []match.Condition) bool {
	return len(conds) != len(withoutDeal(conds))
}

// priceListing prices a just-stored listing. A failure is logged and left
// pending for RetryPricing; it never holds up the poll.
func priceListing(ctx context.Context, app core.App, src, rec *core.Record, seekers []*core.Record) {
	want := pricingSeekers(app, src, rec, seekers)
	if len(want) == 0 {
		return
	}
	if err := Pricing.Observe(ctx, app, rec, want); err != nil {
		app.Logger().Warn("pricing", "listing", rec.Id, "title", rec.GetString("title"), "error", err)
	}
}

// RetryWindow is how long a listing whose classification failed keeps
// being retried; an alert later than that would be stale.
const RetryWindow = 24 * time.Hour

// RetryPricing re-prices listings left pending by a failed classification
// and alerts the deal-seeking seekers they now match. Seekers without deal
// conditions were already decided when the listing was polled; seekers
// created after the listing never alert on it, as in PollSource.
func RetryPricing(ctx context.Context, app core.App) (int, error) {
	if Pricing == nil {
		return 0, nil
	}
	since := time.Now().Add(-RetryWindow).UTC().Format("2006-01-02 15:04:05.000Z")
	recs, err := app.FindRecordsByFilter("listings",
		"created >= {:since} && attrs.pricing.status = {:st}", "created", 200, 0,
		dbx.Params{"since": since, "st": pricing.StatusPending})
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		return 0, nil
	}
	seekers, err := app.FindRecordsByFilter("seekers", "enabled = true", "", 0, 0)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, rec := range recs {
		if ctx.Err() != nil {
			break
		}
		src, err := app.FindRecordById("sources", rec.GetString("source"))
		if err != nil {
			continue
		}
		want := pricingSeekers(app, src, rec, seekers)
		if len(want) == 0 {
			continue
		}
		if err := Pricing.Observe(ctx, app, rec, want); err != nil {
			app.Logger().Warn("pricing retry", "listing", rec.Id, "error", err)
			continue
		}
		done++
		created := rec.GetDateTime("created").Time()
		for _, sk := range want {
			conds, ok := seekerConditions(app, sk)
			if !ok || !hasDeal(conds) || sk.GetDateTime("created").Time().After(created) {
				continue
			}
			res := match.Evaluate(conds, rec)
			if !res.Matched {
				continue
			}
			if err := fireAlert(ctx, app, sk, rec, res); err != nil {
				app.Logger().Error("alert", "seeker", sk.Id, "error", err)
			}
		}
	}
	return done, nil
}

// BackfillStats is what Backfill did.
type BackfillStats struct {
	Scanned   int `json:"scanned"`
	Filed     int `json:"filed"`
	Failed    int `json:"failed"`
	Repriced  int `json:"repriced"`
	NotWanted int `json:"not_wanted"` // no enabled seeker would want its price
}

// Backfill prices stored listings from the last `days` days that were never
// priced (oldest first, so each one's comps are the listings before it),
// then reprices every priced listing in the window against the newest
// research. redo lists statuses (ok, bundle, off_target, unclear, failed)
// to classify again from scratch, e.g. after a prompt change. It never alerts.
// progress, when set, is called per listing.
func Backfill(ctx context.Context, app core.App, days int, redo []string, progress func(string)) (BackfillStats, error) {
	var st BackfillStats
	if Pricing == nil {
		return st, fmt.Errorf("pricing is not configured (SEEKOUT_LLM_URL / SEEKOUT_LLM_KEY)")
	}
	since := time.Now().AddDate(0, 0, -days).UTC().Format("2006-01-02 15:04:05.000Z")
	recs, err := app.FindRecordsByFilter("listings", "created >= {:since} && kind != 'home'", "created", 0, 0, dbx.Params{"since": since})
	if err != nil {
		return st, err
	}
	seekers, err := app.FindRecordsByFilter("seekers", "enabled = true", "", 0, 0)
	if err != nil {
		return st, err
	}
	sources := map[string]*core.Record{}
	for _, rec := range recs {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		st.Scanned++
		status := pricing.Status(rec)
		if status != "" && status != pricing.StatusPending && !slices.Contains(redo, status) {
			continue
		}
		src := sources[rec.GetString("source")]
		if src == nil {
			if src, err = app.FindRecordById("sources", rec.GetString("source")); err != nil {
				continue
			}
			sources[src.Id] = src
		}
		want := pricingSeekers(app, src, rec, seekers)
		if len(want) == 0 {
			st.NotWanted++
			continue
		}
		if status != "" && status != pricing.StatusPending {
			if err := pricing.Unfile(app, rec); err != nil {
				return st, err
			}
		}
		if err := Pricing.Observe(ctx, app, rec, want); err != nil {
			st.Failed++
			if progress != nil {
				progress(fmt.Sprintf("FAILED %s: %v", rec.GetString("title"), err))
			}
			continue
		}
		st.Filed++
		if progress != nil {
			progress(fmt.Sprintf("%-10s %s", pricing.Status(rec), rec.GetString("title")))
		}
	}
	n, err := Reprice(app, days)
	st.Repriced = n
	return st, err
}

// Reprice recomputes attrs.deal for priced listings of the last `days`
// days against the newest research (no model calls).
func Reprice(app core.App, days int) (int, error) {
	if Pricing == nil {
		return 0, fmt.Errorf("pricing is not configured")
	}
	since := time.Now().AddDate(0, 0, -days).UTC().Format("2006-01-02 15:04:05.000Z")
	recs, err := app.FindRecordsByFilter("listings",
		"created >= {:since} && attrs.pricing.status = {:st}", "created", 0, 0,
		dbx.Params{"since": since, "st": pricing.StatusOK})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range recs {
		if err := Pricing.Reprice(app, rec, true); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
