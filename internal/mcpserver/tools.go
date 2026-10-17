package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"gitlab.mauza.net/mauza/seekout/internal/match"
	"gitlab.mauza.net/mauza/seekout/internal/pipeline"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	// searchScan bounds how many recent listings search_listings evaluates
	// conditions against: conditions run in Go (attrs paths, loose typing),
	// not SQL, so the scan has to stop somewhere.
	searchScan = 5000
)

// ---------------------------------------------------------------------------
// Output shapes. Tool results are objects, so lists are wrapped.
// ---------------------------------------------------------------------------

type Source struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Type            string         `json:"type"`
	Kind            string         `json:"kind"`
	Config          map[string]any `json:"config,omitempty"`
	IntervalMinutes int            `json:"interval_minutes"`
	Enabled         bool           `json:"enabled"`
	LastPolled      string         `json:"last_polled,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
}

type Listing struct {
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Price       float64        `json:"price,omitempty"`
	URL         string         `json:"url,omitempty"`
	Description string         `json:"description,omitempty"`
	Published   string         `json:"published,omitempty"`
	Created     string         `json:"created"`
	Attrs       map[string]any `json:"attrs,omitempty"`
	// Score and Why are set by search_listings when conditions include
	// soft ones: the weighted score and its per-condition breakdown.
	Score *float64 `json:"score,omitempty"`
	Why   string   `json:"why,omitempty"`
}

type Seeker struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Sources    []string          `json:"source_ids"`
	Conditions []match.Condition `json:"conditions"`
	Alerters   []string          `json:"alerter_ids"`
	Enabled    bool              `json:"enabled"`
}

type Alerter struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

type Alert struct {
	ID        string           `json:"id"`
	Seeker    string           `json:"seeker_id"`
	Listing   string           `json:"listing_id"`
	Title     string           `json:"title"`
	Body      string           `json:"body"`
	URL       string           `json:"url,omitempty"`
	Delivered []map[string]any `json:"delivered,omitempty"`
	Created   string           `json:"created"`
}

type sourcesOut struct {
	Sources []Source `json:"sources"`
}
type listingsOut struct {
	Listings []Listing `json:"listings"`
	// Scanned is how many stored listings the conditions were evaluated
	// against; it equals the scan cap when older listings went unchecked.
	Scanned int `json:"scanned"`
}
type seekersOut struct {
	Seekers []Seeker `json:"seekers"`
}
type alertersOut struct {
	Alerters []Alerter `json:"alerters"`
}
type alertsOut struct {
	Alerts []Alert `json:"alerts"`
}
type okOut struct {
	OK bool `json:"ok"`
}

// ---------------------------------------------------------------------------
// Inputs.
// ---------------------------------------------------------------------------

type noArgs struct{}

type idArg struct {
	ID string `json:"id" jsonschema:"record id"`
}

type createSourceArgs struct {
	Name            string         `json:"name"`
	Type            string         `json:"type" jsonschema:"rss or ure (utahrealestate.com)"`
	Kind            string         `json:"kind" jsonschema:"home, car, classified or other"`
	Config          map[string]any `json:"config" jsonschema:"rss: {\"url\": feed URL}; ure: {\"type\": residential|multi_unit|land|commercial|farm|rental, \"pages\": 1-N}"`
	IntervalMinutes int            `json:"interval_minutes,omitempty" jsonschema:"poll interval, default 30"`
}

type searchListingsArgs struct {
	Kind       string            `json:"kind,omitempty" jsonschema:"home, car, classified or other"`
	SourceID   string            `json:"source_id,omitempty"`
	Since      string            `json:"since,omitempty" jsonschema:"RFC 3339 time; only listings stored at or after it"`
	Conditions []match.Condition `json:"conditions,omitempty" jsonschema:"AND-ed {field, op, value} conditions, same as a seeker's"`
	Limit      int               `json:"limit,omitempty" jsonschema:"max results, default 20, max 100"`
}

type createSeekerArgs struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind,omitempty" jsonschema:"any (default), home, car, classified or other"`
	SourceIDs  []string          `json:"source_ids,omitempty" jsonschema:"restrict to these sources; empty means all"`
	Conditions []match.Condition `json:"conditions,omitempty" jsonschema:"AND-ed {field, op, value}; empty matches every listing"`
	AlerterIDs []string          `json:"alerter_ids,omitempty" jsonschema:"alerters to notify on a match"`
	Enabled    *bool             `json:"enabled,omitempty" jsonschema:"default true"`
}

// updateSeekerArgs: omitted fields are left unchanged; an empty array clears.
type updateSeekerArgs struct {
	ID         string             `json:"id"`
	Name       *string            `json:"name,omitempty"`
	Kind       *string            `json:"kind,omitempty"`
	SourceIDs  *[]string          `json:"source_ids,omitempty"`
	Conditions *[]match.Condition `json:"conditions,omitempty" jsonschema:"replaces the whole condition list"`
	AlerterIDs *[]string          `json:"alerter_ids,omitempty"`
	Enabled    *bool              `json:"enabled,omitempty"`
}

type listAlertsArgs struct {
	SeekerID string `json:"seeker_id,omitempty"`
	Since    string `json:"since,omitempty" jsonschema:"RFC 3339 time"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max results, default 20, max 100"`
}

// ---------------------------------------------------------------------------
// Registration.
// ---------------------------------------------------------------------------

func registerTools(s *mcp.Server, app core.App) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "list_sources", Annotations: ro,
		Description: "List the configured listing sources (shared by all users) with their poll status."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, sourcesOut, error) {
			if _, err := userID(req); err != nil {
				return nil, sourcesOut{}, err
			}
			recs, err := app.FindRecordsByFilter("sources", "", "name", 0, 0)
			if err != nil {
				return nil, sourcesOut{}, err
			}
			out := sourcesOut{Sources: make([]Source, 0, len(recs))}
			for _, r := range recs {
				out.Sources = append(out.Sources, toSource(r))
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "create_source",
		Description: "Add a listing source. Its first poll only ingests the existing backlog; alerts start with listings that appear after that."},
		func(ctx context.Context, req *mcp.CallToolRequest, in createSourceArgs) (*mcp.CallToolResult, Source, error) {
			if _, err := userID(req); err != nil {
				return nil, Source{}, err
			}
			col, err := app.FindCollectionByNameOrId("sources")
			if err != nil {
				return nil, Source{}, err
			}
			interval := in.IntervalMinutes
			if interval <= 0 {
				interval = 30
			}
			rec := core.NewRecord(col)
			rec.Set("name", in.Name)
			rec.Set("type", in.Type)
			rec.Set("kind", in.Kind)
			rec.Set("config", in.Config)
			rec.Set("interval_minutes", interval)
			rec.Set("enabled", true)
			if err := app.Save(rec); err != nil {
				return nil, Source{}, err
			}
			return nil, toSource(rec), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "poll_source",
		Description: "Poll one source now instead of waiting for its interval. New matching listings fire alerts as usual."},
		func(ctx context.Context, req *mcp.CallToolRequest, in idArg) (*mcp.CallToolResult, pipeline.Stats, error) {
			if _, err := userID(req); err != nil {
				return nil, pipeline.Stats{}, err
			}
			src, err := app.FindRecordById("sources", in.ID)
			if err != nil {
				return nil, pipeline.Stats{}, notFound("source", in.ID, err)
			}
			// Detached like the HTTP poll route: a client that gives up must
			// not cancel alert delivery half way.
			pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			stats, err := pipeline.PollSource(pctx, app, src)
			return nil, stats, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "search_listings", Annotations: ro,
		Description: "Search stored listings, newest first, with optional kind/source/since filters and seeker-style conditions. Use it to try conditions out before saving them as a seeker."},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchListingsArgs) (*mcp.CallToolResult, listingsOut, error) {
			if _, err := userID(req); err != nil {
				return nil, listingsOut{}, err
			}
			if err := match.Validate(in.Conditions); err != nil {
				return nil, listingsOut{}, err
			}
			filters, params := []string{}, dbx.Params{}
			if in.Kind != "" {
				filters, params["kind"] = append(filters, "kind = {:kind}"), in.Kind
			}
			if in.SourceID != "" {
				filters, params["source"] = append(filters, "source = {:source}"), in.SourceID
			}
			if in.Since != "" {
				since, err := parseTime(in.Since)
				if err != nil {
					return nil, listingsOut{}, err
				}
				filters, params["since"] = append(filters, "created >= {:since}"), since
			}
			recs, err := app.FindRecordsByFilter("listings", strings.Join(filters, " && "), "-created", searchScan, 0, params)
			if err != nil {
				return nil, listingsOut{}, err
			}
			limit := clampLimit(in.Limit)
			out := listingsOut{Listings: []Listing{}}
			for _, r := range recs {
				out.Scanned++
				res := match.Evaluate(in.Conditions, r)
				if !res.Matched {
					continue
				}
				l := toListing(r)
				if len(res.Parts) > 0 {
					l.Score, l.Why = &res.Score, res.Explain()
				}
				out.Listings = append(out.Listings, l)
				if len(out.Listings) == limit {
					break
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_listing", Annotations: ro,
		Description: "Fetch one listing with its full description and attrs."},
		func(ctx context.Context, req *mcp.CallToolRequest, in idArg) (*mcp.CallToolResult, Listing, error) {
			if _, err := userID(req); err != nil {
				return nil, Listing{}, err
			}
			r, err := app.FindRecordById("listings", in.ID)
			if err != nil {
				return nil, Listing{}, notFound("listing", in.ID, err)
			}
			return nil, toListing(r), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_seekers", Annotations: ro,
		Description: "List your seekers (saved searches that alert on new matching listings)."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, seekersOut, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, seekersOut{}, err
			}
			recs, err := app.FindRecordsByFilter("seekers", "user = {:u}", "name", 0, 0, dbx.Params{"u": uid})
			if err != nil {
				return nil, seekersOut{}, err
			}
			out := seekersOut{Seekers: make([]Seeker, 0, len(recs))}
			for _, r := range recs {
				out.Seekers = append(out.Seekers, toSeeker(r))
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "create_seeker",
		Description: "Create a seeker. It alerts on listings that arrive from now on; it does not backfill."},
		func(ctx context.Context, req *mcp.CallToolRequest, in createSeekerArgs) (*mcp.CallToolResult, Seeker, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, Seeker{}, err
			}
			col, err := app.FindCollectionByNameOrId("seekers")
			if err != nil {
				return nil, Seeker{}, err
			}
			rec := core.NewRecord(col)
			rec.Set("user", uid)
			rec.Set("kind", "any")
			rec.Set("enabled", true)
			upd := updateSeekerArgs{
				Name: &in.Name, SourceIDs: &in.SourceIDs, Conditions: &in.Conditions,
				AlerterIDs: &in.AlerterIDs, Enabled: in.Enabled,
			}
			if in.Kind != "" {
				upd.Kind = &in.Kind
			}
			if err := applySeeker(app, uid, rec, upd); err != nil {
				return nil, Seeker{}, err
			}
			return nil, toSeeker(rec), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "update_seeker",
		Description: "Change one of your seekers. Omitted fields stay as they are; conditions, source_ids and alerter_ids replace the whole list."},
		func(ctx context.Context, req *mcp.CallToolRequest, in updateSeekerArgs) (*mcp.CallToolResult, Seeker, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, Seeker{}, err
			}
			rec, err := ownRecord(app, "seekers", in.ID, uid)
			if err != nil {
				return nil, Seeker{}, err
			}
			if err := applySeeker(app, uid, rec, in); err != nil {
				return nil, Seeker{}, err
			}
			return nil, toSeeker(rec), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "delete_seeker",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)},
		Description: "Delete one of your seekers and its alert history."},
		func(ctx context.Context, req *mcp.CallToolRequest, in idArg) (*mcp.CallToolResult, okOut, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, okOut{}, err
			}
			rec, err := ownRecord(app, "seekers", in.ID, uid)
			if err != nil {
				return nil, okOut{}, err
			}
			if err := app.Delete(rec); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_alerters", Annotations: ro,
		Description: "List your notification targets (ntfy, webhook). Their config holds credentials and is not returned."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, alertersOut, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, alertersOut{}, err
			}
			recs, err := app.FindRecordsByFilter("alerters", "user = {:u}", "name", 0, 0, dbx.Params{"u": uid})
			if err != nil {
				return nil, alertersOut{}, err
			}
			out := alertersOut{Alerters: make([]Alerter, 0, len(recs))}
			for _, r := range recs {
				out.Alerters = append(out.Alerters, Alerter{
					ID: r.Id, Name: r.GetString("name"), Type: r.GetString("type"), Enabled: r.GetBool("enabled"),
				})
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "test_alerter",
		Description: "Send a test notification through one of your alerters."},
		func(ctx context.Context, req *mcp.CallToolRequest, in idArg) (*mcp.CallToolResult, okOut, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, okOut{}, err
			}
			if _, err := ownRecord(app, "alerters", in.ID, uid); err != nil {
				return nil, okOut{}, err
			}
			if err := pipeline.TestAlerter(ctx, app, in.ID); err != nil {
				return nil, okOut{}, err
			}
			return nil, okOut{OK: true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_alerts", Annotations: ro,
		Description: "List alerts your seekers fired, newest first, with per-alerter delivery results."},
		func(ctx context.Context, req *mcp.CallToolRequest, in listAlertsArgs) (*mcp.CallToolResult, alertsOut, error) {
			uid, err := userID(req)
			if err != nil {
				return nil, alertsOut{}, err
			}
			filters, params := []string{"user = {:u}"}, dbx.Params{"u": uid}
			if in.SeekerID != "" {
				filters, params["seeker"] = append(filters, "seeker = {:seeker}"), in.SeekerID
			}
			if in.Since != "" {
				since, err := parseTime(in.Since)
				if err != nil {
					return nil, alertsOut{}, err
				}
				filters, params["since"] = append(filters, "created >= {:since}"), since
			}
			recs, err := app.FindRecordsByFilter("alerts", strings.Join(filters, " && "), "-created", clampLimit(in.Limit), 0, params)
			if err != nil {
				return nil, alertsOut{}, err
			}
			out := alertsOut{Alerts: make([]Alert, 0, len(recs))}
			for _, r := range recs {
				a := Alert{
					ID: r.Id, Seeker: r.GetString("seeker"), Listing: r.GetString("listing"),
					Title: r.GetString("title"), Body: r.GetString("body"), URL: r.GetString("url"),
					Created: r.GetDateTime("created").String(),
				}
				_ = r.UnmarshalJSONField("delivered", &a.Delivered)
				out.Alerts = append(out.Alerts, a)
			}
			return nil, out, nil
		})
}

// applySeeker validates and saves the set fields of upd onto rec. Alerters
// must belong to the caller: PocketBase's relation check only proves they
// exist, and the collection rules that would stop a cross-user reference
// don't run for server-side saves.
func applySeeker(app core.App, uid string, rec *core.Record, upd updateSeekerArgs) error {
	if upd.Name != nil {
		rec.Set("name", *upd.Name)
	}
	if upd.Kind != nil {
		rec.Set("kind", *upd.Kind)
	}
	if upd.SourceIDs != nil {
		rec.Set("sources", *upd.SourceIDs)
	}
	if upd.Conditions != nil {
		if err := match.Validate(*upd.Conditions); err != nil {
			return err
		}
		rec.Set("conditions", *upd.Conditions)
	}
	if upd.AlerterIDs != nil {
		for _, id := range *upd.AlerterIDs {
			if _, err := ownRecord(app, "alerters", id, uid); err != nil {
				return err
			}
		}
		rec.Set("alerters", *upd.AlerterIDs)
	}
	if upd.Enabled != nil {
		rec.Set("enabled", *upd.Enabled)
	}
	return app.Save(rec)
}

// ownRecord loads a per-user record, answering "not found" for other users'
// records so ids can't be probed.
func ownRecord(app core.App, collection, id, uid string) (*core.Record, error) {
	singular := strings.TrimSuffix(collection, "s")
	rec, err := app.FindRecordById(collection, id)
	if err != nil {
		return nil, notFound(singular, id, err)
	}
	if rec.GetString("user") != uid {
		return nil, notFound(singular, id, sql.ErrNoRows)
	}
	return rec, nil
}

func notFound(what, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s %q not found", what, id)
	}
	return err
}

func parseTime(s string) (string, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("since: want RFC 3339 (e.g. 2026-10-01T00:00:00Z): %w", err)
	}
	return t.UTC().Format("2006-01-02 15:04:05.000Z"), nil
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

func ptr[T any](v T) *T { return &v }

func toSource(r *core.Record) Source {
	s := Source{
		ID: r.Id, Name: r.GetString("name"), Type: r.GetString("type"), Kind: r.GetString("kind"),
		IntervalMinutes: r.GetInt("interval_minutes"), Enabled: r.GetBool("enabled"),
		LastError: r.GetString("last_error"),
	}
	_ = r.UnmarshalJSONField("config", &s.Config)
	if t := r.GetDateTime("last_polled"); !t.IsZero() {
		s.LastPolled = t.String()
	}
	return s
}

func toListing(r *core.Record) Listing {
	l := Listing{
		ID: r.Id, Source: r.GetString("source"), Kind: r.GetString("kind"), Title: r.GetString("title"),
		Price: r.GetFloat("price"), URL: r.GetString("url"), Description: r.GetString("description"),
		Created: r.GetDateTime("created").String(),
	}
	if t := r.GetDateTime("published"); !t.IsZero() {
		l.Published = t.String()
	}
	_ = r.UnmarshalJSONField("attrs", &l.Attrs)
	return l
}

func toSeeker(r *core.Record) Seeker {
	s := Seeker{
		ID: r.Id, Name: r.GetString("name"), Kind: r.GetString("kind"),
		Sources: r.GetStringSlice("sources"), Alerters: r.GetStringSlice("alerters"),
		Enabled: r.GetBool("enabled"), Conditions: []match.Condition{},
	}
	_ = r.UnmarshalJSONField("conditions", &s.Conditions)
	return s
}
