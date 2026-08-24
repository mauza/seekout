package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		loggedIn := `@request.auth.id != ""`

		// ---------------------------------------------------------------
		// alerters — per-user notification targets (ntfy, webhook)
		// ---------------------------------------------------------------
		alerters := core.NewBaseCollection("alerters")
		alerters.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, Required: true},
			&core.TextField{Name: "name", Required: true},
			&core.SelectField{Name: "type", Required: true, Values: []string{"ntfy", "webhook"}},
			&core.JSONField{Name: "config"},
			&core.BoolField{Name: "enabled"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		alerterOwner := `@request.auth.id = user`
		alerters.ListRule = &alerterOwner
		alerters.ViewRule = &alerterOwner
		alerters.CreateRule = &alerterOwner
		alerters.UpdateRule = &alerterOwner
		alerters.DeleteRule = &alerterOwner
		if err := app.Save(alerters); err != nil {
			return err
		}

		// ---------------------------------------------------------------
		// sources — configured site pollers (rss, scrapers, ...)
		// ---------------------------------------------------------------
		sources := core.NewBaseCollection("sources")
		sources.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.SelectField{Name: "type", Required: true, Values: []string{"rss"}},
			&core.SelectField{Name: "kind", Required: true, Values: []string{"home", "car", "classified", "other"}},
			&core.JSONField{Name: "config"},
			&core.NumberField{Name: "interval_minutes", Required: true},
			&core.BoolField{Name: "enabled"},
			&core.DateField{Name: "last_polled"},
			&core.TextField{Name: "last_error"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		sources.ListRule = &loggedIn
		sources.ViewRule = &loggedIn
		sources.CreateRule = &loggedIn
		sources.UpdateRule = &loggedIn
		sources.DeleteRule = &loggedIn
		if err := app.Save(sources); err != nil {
			return err
		}

		// ---------------------------------------------------------------
		// listings — normalized records produced by sources
		// ---------------------------------------------------------------
		listings := core.NewBaseCollection("listings")
		listings.Fields.Add(
			&core.RelationField{Name: "source", CollectionId: sources.Id, Required: true},
			&core.TextField{Name: "external_id", Required: true},
			&core.SelectField{Name: "kind", Values: []string{"home", "car", "classified", "other"}},
			&core.TextField{Name: "title"},
			&core.NumberField{Name: "price"},
			&core.URLField{Name: "url"},
			&core.URLField{Name: "image"},
			&core.TextField{Name: "description"},
			&core.DateField{Name: "published"},
			&core.JSONField{Name: "attrs"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		listings.AddIndex("idx_listings_source_external", true, "source, external_id", "")
		listings.ListRule = &loggedIn
		listings.ViewRule = &loggedIn
		if err := app.Save(listings); err != nil {
			return err
		}

		// ---------------------------------------------------------------
		// seekers — per-user matching rules over listings
		// ---------------------------------------------------------------
		seekers := core.NewBaseCollection("seekers")
		seekers.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, Required: true},
			&core.TextField{Name: "name", Required: true},
			&core.SelectField{Name: "kind", Required: true, Values: []string{"any", "home", "car", "classified", "other"}},
			&core.RelationField{Name: "sources", CollectionId: sources.Id, MaxSelect: 100},
			&core.JSONField{Name: "conditions"},
			&core.RelationField{Name: "alerters", CollectionId: alerters.Id, MaxSelect: 100},
			&core.BoolField{Name: "enabled"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		seekerOwner := `@request.auth.id = user`
		seekers.ListRule = &seekerOwner
		seekers.ViewRule = &seekerOwner
		seekers.CreateRule = &seekerOwner
		seekers.UpdateRule = &seekerOwner
		seekers.DeleteRule = &seekerOwner
		if err := app.Save(seekers); err != nil {
			return err
		}

		// ---------------------------------------------------------------
		// alerts — one record per (seeker, listing) match
		// ---------------------------------------------------------------
		alerts := core.NewBaseCollection("alerts")
		alerts.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, Required: true},
			&core.RelationField{Name: "seeker", CollectionId: seekers.Id, Required: true},
			&core.RelationField{Name: "listing", CollectionId: listings.Id, Required: true},
			&core.TextField{Name: "title"},
			&core.TextField{Name: "body"},
			&core.URLField{Name: "url"},
			&core.JSONField{Name: "delivered"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		alerts.AddIndex("idx_alerts_seeker_listing", true, "seeker, listing", "")
		alertOwner := `@request.auth.id = user`
		alerts.ListRule = &alertOwner
		alerts.ViewRule = &alertOwner
		if err := app.Save(alerts); err != nil {
			return err
		}

		return nil
	}, func(app core.App) error {
		for _, name := range []string{"alerts", "seekers", "listings", "sources", "alerters"} {
			c, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if err := app.Delete(c); err != nil {
				return err
			}
		}
		return nil
	}, "1700000000_init.go")
}
