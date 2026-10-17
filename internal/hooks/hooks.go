// Package hooks holds the record rules PocketBase's collection API rules
// can't express (including invite-only signup, invites.go), plus the boot-time schema sync that lets a source type,
// alerter type or listing kind be added without a migration.
//
// The validate hooks run on every save that goes through validation: the
// REST API (web UI), MCP tools and server code alike. Poll bookkeeping
// (pipeline.finishSource) saves without validation on purpose, so a record
// made invalid by a later rule change still records its polls.
package hooks

import (
	"fmt"
	"slices"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/alerters"
	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/match"
	"github.com/mauza/seekout/internal/netguard"
	"github.com/mauza/seekout/internal/plugin"
	"github.com/mauza/seekout/internal/sources"
)

// Bind registers the validate hooks and invite-only signup on app.
func Bind(app core.App) {
	bindInvites(app)

	// Seeker conditions are free-form JSON, so a malformed one (a typo'd op,
	// a soft condition without floor/best) would otherwise be stored and
	// then silently never match.
	app.OnRecordValidate("seekers").BindFunc(func(e *core.RecordEvent) error {
		var conds []match.Condition
		if err := e.Record.UnmarshalJSONField("conditions", &conds); err != nil {
			return fieldErr("conditions", "conditions: "+err.Error())
		}
		if err := match.Validate(conds); err != nil {
			return fieldErr("conditions", err.Error())
		}
		// The relation check only proves the alerters exist and the API rule
		// only checks the seeker's own user, so without this a seeker could
		// notify through someone else's alerter. Same answer as a missing
		// id, so other users' ids can't be probed.
		for _, id := range e.Record.GetStringSlice("alerters") {
			a, err := e.App.FindRecordById("alerters", id)
			if err != nil || a.GetString("user") != e.Record.GetString("user") {
				return fieldErr("alerters", fmt.Sprintf("alerter %q not found", id))
			}
		}
		return e.Next()
	})

	app.OnRecordValidate("sources").BindFunc(func(e *core.RecordEvent) error {
		if err := validateConfig(e.Record, sources.Info); err != nil {
			return err
		}
		return e.Next()
	})
	app.OnRecordValidate("alerters").BindFunc(func(e *core.RecordEvent) error {
		if err := validateConfig(e.Record, alerters.Info); err != nil {
			return err
		}
		// Alerter targets are user-supplied; internal ones are refused
		// again on every send (netguard.Client), this is the clear error.
		info, _ := alerters.Info(e.Record.GetString("type"))
		var cfg map[string]any
		_ = e.Record.UnmarshalJSONField("config", &cfg)
		for _, f := range info.Fields {
			if u, _ := cfg[f.Key].(string); f.Type == plugin.URL && u != "" {
				if err := netguard.CheckURL(e.Context, u); err != nil {
					return fieldErr("config", f.Key+": "+err.Error())
				}
			}
		}
		return e.Next()
	})
}

// validateConfig checks a source or alerter record's config against its
// type's declared fields, so a missing feed URL or ntfy topic is a 400 on
// save instead of a last_error or a failed alert later.
func validateConfig(rec *core.Record, info func(string) (plugin.Info, bool)) error {
	typ := rec.GetString("type")
	in, ok := info(typ)
	if !ok {
		return fieldErr("type", fmt.Sprintf("unknown type %q", typ))
	}
	var cfg map[string]any
	if err := rec.UnmarshalJSONField("config", &cfg); err != nil {
		return fieldErr("config", "config must be a JSON object")
	}
	if err := in.Validate(cfg); err != nil {
		return fieldErr("config", err.Error())
	}
	return nil
}

func fieldErr(field, msg string) error {
	return validation.Errors{field: validation.NewError("validation_"+field, msg)}
}

// SyncSelects adds every registered source type, alerter type and listing
// kind to the select fields that store them. It only ever adds values, so
// records of a type that has since been removed stay readable. Call after
// migrations have run.
func SyncSelects(app core.App) error {
	names := func(infos []plugin.Info) []string {
		out := make([]string, len(infos))
		for i, in := range infos {
			out[i] = in.Type
		}
		return out
	}
	want := []struct {
		collection, field string
		values            []string
	}{
		{"sources", "type", names(sources.Infos())},
		{"alerters", "type", names(alerters.Infos())},
		{"sources", "kind", listing.Kinds},
		{"listings", "kind", listing.Kinds},
		{"seekers", "kind", append([]string{"any"}, listing.Kinds...)},
	}
	for _, w := range want {
		col, err := app.FindCollectionByNameOrId(w.collection)
		if err != nil {
			return err
		}
		sel, ok := col.Fields.GetByName(w.field).(*core.SelectField)
		if !ok {
			return fmt.Errorf("%s.%s is not a select field", w.collection, w.field)
		}
		changed := false
		for _, v := range w.values {
			if !slices.Contains(sel.Values, v) {
				sel.Values = append(sel.Values, v)
				changed = true
			}
		}
		if changed {
			if err := app.Save(col); err != nil {
				return fmt.Errorf("add %s.%s values: %w", w.collection, w.field, err)
			}
		}
	}
	return nil
}
