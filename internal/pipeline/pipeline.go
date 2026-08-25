// Package pipeline wires sources → dedup → matcher → alerters. It replaces
// the oprah Pub/Sub fan-out with direct in-process calls.
package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"gitlab.mauza.net/mauza/seekout/internal/alerters"
	"gitlab.mauza.net/mauza/seekout/internal/listing"
	"gitlab.mauza.net/mauza/seekout/internal/match"
	"gitlab.mauza.net/mauza/seekout/internal/sources"
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
		if err != nil {
			app.Logger().Error("poll failed", "source", src.GetString("name"), "error", err)
		} else {
			app.Logger().Info("poll complete",
				"source", stats.Source, "fetched", stats.Fetched, "new", stats.New,
				"matched", stats.Matched, "seeded", stats.Seeded)
		}
	}
}

// PollSource fetches one source, stores new listings, and fires alerts for
// every matching seeker. It always records last_polled/last_error on the
// source so failures don't retry on every cron tick.
//
// The first successful poll of a source only ingests: a feed's existing
// backlog is not news, and alerting on it would fire dozens of
// notifications the moment a source is added.
func PollSource(ctx context.Context, app core.App, src *core.Record) (Stats, error) {
	stats := Stats{Source: src.GetString("name")}

	impl, err := sources.For(src)
	if err != nil {
		return stats, finishSource(app, src, err)
	}

	listings, err := impl.Poll(ctx, src)
	if err != nil {
		return stats, finishSource(app, src, err)
	}
	stats.Fetched = len(listings)

	seeding := !src.GetBool("seeded")

	var seekers []*core.Record
	if !seeding {
		seekers, err = app.FindRecordsByFilter("seekers", "enabled = true", "", 0, 0)
		if err != nil {
			return stats, finishSource(app, src, fmt.Errorf("list seekers: %w", err))
		}
	}

	for _, l := range listings {
		rec, isNew, err := saveListing(app, src, l)
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
			if !match.Matches(conds, rec) {
				continue
			}
			if err := fireAlert(ctx, app, seeker, rec); err != nil {
				app.Logger().Error("alert", "seeker", seeker.Id, "error", err)
			}
			stats.Matched++
		}
	}

	stats.Seeded = seeding
	if seeding {
		src.Set("seeded", true)
	}
	return stats, finishSource(app, src, nil)
}

// saveListing inserts the listing unless (source, external_id) already
// exists. Returns the record and false for duplicates.
func saveListing(app core.App, src *core.Record, l listing.Listing) (*core.Record, bool, error) {
	existing, err := app.FindFirstRecordByFilter("listings",
		"source = {:s} && external_id = {:e}",
		dbx.Params{"s": src.Id, "e": l.ExternalID})
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
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
func fireAlert(ctx context.Context, app core.App, seeker, listingRec *core.Record) error {
	col, err := app.FindCollectionByNameOrId("alerts")
	if err != nil {
		return err
	}
	alert := alerters.Alert{
		Title: seeker.GetString("name"),
		Body:  alertBody(listingRec),
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

// TestAlerter sends a sample notification through one alerter record.
func TestAlerter(ctx context.Context, app core.App, id string) error {
	rec, err := app.FindRecordById("alerters", id)
	if err != nil {
		return fmt.Errorf("alerter not found: %w", err)
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

func alertBody(rec *core.Record) string {
	body := rec.GetString("title")
	if price := rec.GetFloat("price"); price > 0 {
		body = fmt.Sprintf("%s — $%.0f", body, price)
	}
	return body
}

func finishSource(app core.App, src *core.Record, pollErr error) error {
	src.Set("last_polled", time.Now().UTC().Format("2006-01-02 15:04:05.000Z"))
	errStr := ""
	if pollErr != nil {
		errStr = pollErr.Error()
	}
	src.Set("last_error", errStr)
	if err := app.Save(src); err != nil {
		return errors.Join(pollErr, err)
	}
	return pollErr
}
