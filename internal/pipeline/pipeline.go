// Package pipeline is seekout's one data path, run per source poll:
//
//	source.Poll → dedupe on (source, external_id) → Enrich new listings
//	→ store → match every enabled seeker → alert record + alerter fan-out
//
// It is driven by the minute cron (PollDue) and by on-demand polls (the
// web UI's "poll now" and MCP poll_source), all through PollSource.
package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/alerters"
	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/match"
	"github.com/mauza/seekout/internal/sources"
)

// Stats summarizes one source poll (also the poll endpoint's response).
type Stats struct {
	Source  string `json:"source"`
	Fetched int    `json:"fetched"`
	New     int    `json:"new"`
	Matched int    `json:"matched"`
	Seeded  bool   `json:"seeded"`
}

// PollDue is the cron entrypoint: poll every enabled source whose
// interval_minutes has elapsed since last_polled. Errors are recorded on the
// source record and logged, never propagated — one bad source must not
// starve the others.
func PollDue(ctx context.Context, app core.App) {
	srcs, err := app.FindRecordsByFilter("sources", "enabled = true", "", 0, 0)
	if err != nil {
		app.Logger().Error("poll: list sources", "error", err)
		return
	}
	now := time.Now()
	for _, src := range srcs {
		interval := time.Duration(src.GetInt("interval_minutes")) * time.Minute
		if interval <= 0 {
			interval = 30 * time.Minute
		}
		if last := src.GetDateTime("last_polled"); !last.IsZero() && now.Sub(last.Time()) < interval {
			continue
		}
		stats, err := PollSource(ctx, app, src)
		if errors.Is(err, ErrBusy) {
			continue
		}
		if err != nil {
			app.Logger().Error("poll failed", "source", src.GetString("name"), "error", err)
		} else {
			app.Logger().Info("poll complete",
				"source", stats.Source, "fetched", stats.Fetched, "new", stats.New,
				"matched", stats.Matched, "seeded", stats.Seeded)
		}
	}
}

// ErrBusy is returned by PollSource when the source is already being polled.
var ErrBusy = errors.New("source is already being polled")

// polling holds the ids of sources with a poll in flight. The cron fires
// PollDue every minute without waiting for the previous run, and last_polled
// is only written when a poll ends, so a poll slower than a minute (detail
// enrichment) would otherwise be started again on top of itself.
var polling sync.Map

// PollSource fetches one source, stores new listings, and fires alerts for
// every matching seeker. It always records last_polled/last_error on the
// source so failures don't retry on every cron tick.
//
// The first successful poll of a source only ingests: a feed's existing
// backlog is not news, and alerting on it would fire dozens of
// notifications the moment a source is added.
func PollSource(ctx context.Context, app core.App, src *core.Record) (Stats, error) {
	stats := Stats{Source: src.GetString("name")}
	if _, busy := polling.LoadOrStore(src.Id, true); busy {
		return stats, ErrBusy
	}
	defer polling.Delete(src.Id)

	impl, err := sources.For(src)
	if err != nil {
		return stats, finishSource(app, src, false, err)
	}

	listings, err := impl.Poll(ctx, src)
	if err != nil {
		return stats, finishSource(app, src, false, err)
	}
	stats.Fetched = len(listings)

	seeding := !src.GetBool("seeded")

	var seekers []*core.Record
	if !seeding {
		seekers, err = app.FindRecordsByFilter("seekers", "enabled = true", "", 0, 0)
		if err != nil {
			return stats, finishSource(app, src, false, fmt.Errorf("list seekers: %w", err))
		}
	}

	// Detail enrichment costs a request per listing, so it runs only for
	// listings not stored yet. A listing whose enrichment fails is not
	// stored either: the next poll sees it as new and retries.
	enricher, _ := impl.(sources.Enricher)
	var enrichErrs []error

	for _, l := range listings {
		rec, isNew, err := saveListing(ctx, app, src, l, enricher)
		if errors.Is(err, errEnrich) {
			enrichErrs = append(enrichErrs, err)
			continue
		}
		if err != nil {
			app.Logger().Error("save listing", "source", stats.Source, "external_id", l.ExternalID, "error", err)
			continue
		}
		if !isNew {
			continue
		}
		stats.New++

		for _, seeker := range seekers {
			if !seekerApplies(seeker, src, rec.GetString("kind")) {
				continue
			}
			var conds []match.Condition
			if err := seeker.UnmarshalJSONField("conditions", &conds); err != nil {
				app.Logger().Error("seeker conditions", "seeker", seeker.Id, "error", err)
				continue
			}
			res := match.Evaluate(conds, rec)
			if !res.Matched {
				continue
			}
			if err := fireAlert(ctx, app, seeker, rec, res); err != nil {
				app.Logger().Error("alert", "seeker", seeker.Id, "error", err)
			}
			stats.Matched++
		}
	}

	stats.Seeded = seeding
	var pollErr error
	if len(enrichErrs) > 0 {
		pollErr = fmt.Errorf("%d new listing(s) skipped until next poll: %w", len(enrichErrs), enrichErrs[0])
	}
	// Seeded even when some listings failed enrichment: those are retried
	// and would then alert as new, but one listing that never enriches
	// must not keep the whole source from ever alerting.
	return stats, finishSource(app, src, seeding, pollErr)
}

var errEnrich = errors.New("enrich")

// saveListing inserts the listing unless (source, external_id) already
// exists. Returns the record and false for duplicates. A non-nil enricher
// fills in the new listing before the insert; its failure wraps errEnrich.
func saveListing(ctx context.Context, app core.App, src *core.Record, l listing.Listing, enricher sources.Enricher) (*core.Record, bool, error) {
	existing, err := app.FindFirstRecordByFilter("listings",
		"source = {:s} && external_id = {:e}",
		dbx.Params{"s": src.Id, "e": l.ExternalID})
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	if enricher != nil {
		if err := enricher.Enrich(ctx, src, &l); err != nil {
			return nil, false, fmt.Errorf("%w: %s: %w", errEnrich, l.ExternalID, err)
		}
	}

	col, err := app.FindCollectionByNameOrId("listings")
	if err != nil {
		return nil, false, err
	}
	rec := core.NewRecord(col)
	rec.Set("source", src.Id)
	rec.Set("external_id", l.ExternalID)
	rec.Set("kind", src.GetString("kind"))
	rec.Set("title", l.Title)
	rec.Set("price", l.Price)
	rec.Set("url", l.URL)
	rec.Set("image", l.Image)
	rec.Set("description", l.Description)
	rec.Set("attrs", l.Attrs)
	if !l.Published.IsZero() {
		rec.Set("published", l.Published.UTC().Format("2006-01-02 15:04:05.000Z"))
	}
	if err := app.Save(rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

// seekerApplies gates on kind and the optional sources allow-list.
func seekerApplies(seeker, src *core.Record, kind string) bool {
	if k := seeker.GetString("kind"); k != "" && k != "any" && k != kind {
		return false
	}
	if sids := seeker.GetStringSlice("sources"); len(sids) > 0 && !slices.Contains(sids, src.Id) {
		return false
	}
	return true
}

// fireAlert persists the alert record and fans out to the seeker's alerters.
func fireAlert(ctx context.Context, app core.App, seeker, listingRec *core.Record, res match.Result) error {
	col, err := app.FindCollectionByNameOrId("alerts")
	if err != nil {
		return err
	}
	alert := alerters.Alert{
		Title: seeker.GetString("name"),
		Body:  alertBody(listingRec, res),
		URL:   listingRec.GetString("url"),
		Image: listingRec.GetString("image"),
	}

	rec := core.NewRecord(col)
	rec.Set("user", seeker.GetString("user"))
	rec.Set("seeker", seeker.Id)
	rec.Set("listing", listingRec.Id)
	if _, err := app.FindFirstRecordByFilter("alerts",
		"seeker = {:s} && listing = {:l}",
		dbx.Params{"s": seeker.Id, "l": listingRec.Id}); err == nil {
		return nil // already alerted for this pair
	}

	rec.Set("title", listingRec.GetString("title"))
	rec.Set("body", alert.Body)
	rec.Set("url", alert.URL)

	delivered := make([]map[string]any, 0)
	for _, aid := range seeker.GetStringSlice("alerters") {
		entry := map[string]any{"alerter": aid, "ok": false}
		delivered = append(delivered, entry)

		arec, err := app.FindRecordById("alerters", aid)
		if err != nil {
			entry["error"] = "alerter not found"
			continue
		}
		entry["name"] = arec.GetString("name")
		// The seekers validate hook stops this on save; checked again here
		// for records saved before it existed.
		if arec.GetString("user") != seeker.GetString("user") {
			entry["error"] = "alerter belongs to another user"
			continue
		}
		if !arec.GetBool("enabled") {
			entry["error"] = "disabled"
			continue
		}
		impl, err := alerters.For(arec)
		if err != nil {
			entry["error"] = err.Error()
			continue
		}
		if err := impl.Send(ctx, arec, alert); err != nil {
			entry["error"] = err.Error()
			continue
		}
		entry["ok"] = true
	}
	rec.Set("delivered", delivered)

	if err := app.Save(rec); err != nil {
		return err
	}
	return nil
}

// TestAlerter sends a sample notification through one of userID's alerters.
func TestAlerter(ctx context.Context, app core.App, id, userID string) error {
	rec, err := app.FindRecordById("alerters", id)
	if err != nil || rec.GetString("user") != userID {
		return fmt.Errorf("alerter %q not found", id)
	}
	impl, err := alerters.For(rec)
	if err != nil {
		return err
	}
	return impl.Send(ctx, rec, alerters.Alert{
		Title: "Seekout test",
		Body:  "Your alerter is wired up.",
		URL:   "https://seekout.mau.guru",
	})
}

// alertBody is the listing title and price, plus the score breakdown for
// a seeker with soft conditions so the notification says what traded off.
func alertBody(rec *core.Record, res match.Result) string {
	body := rec.GetString("title")
	if price := rec.GetFloat("price"); price > 0 {
		body = fmt.Sprintf("%s — $%.0f", body, price)
	}
	if why := res.Explain(); why != "" {
		body += "\n" + why
	}
	return body
}

// finishSource records the poll on the source: last_polled, last_error and,
// once the first poll has ingested the backlog, seeded. It writes onto a fresh copy of the
// record because src was loaded before a poll that can take minutes; saving
// src itself would revert anything edited meanwhile (a paused source, a new
// feed URL). The fields are mirrored onto src for the caller.
func finishSource(app core.App, src *core.Record, seeded bool, pollErr error) error {
	fields := map[string]any{
		"last_polled": time.Now().UTC().Format("2006-01-02 15:04:05.000Z"),
		"last_error":  "",
	}
	if pollErr != nil {
		fields["last_error"] = pollErr.Error()
	}
	if seeded {
		fields["seeded"] = true
	}
	for k, v := range fields {
		src.Set(k, v)
	}
	fresh, err := app.FindRecordById("sources", src.Id)
	if errors.Is(err, sql.ErrNoRows) {
		return pollErr // deleted mid-poll; nothing to record
	}
	if err != nil {
		return errors.Join(pollErr, err)
	}
	for k, v := range fields {
		fresh.Set(k, v)
	}
	// No validation: bookkeeping must land even on a source whose config a
	// newer rule would reject, or it would be re-polled every minute.
	if err := app.SaveNoValidate(fresh); err != nil {
		return errors.Join(pollErr, err)
	}
	return pollErr
}
