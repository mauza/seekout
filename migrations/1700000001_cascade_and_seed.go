package migrations

import (
	"fmt"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// Derived records must not pin their parents alive. Without cascadeDelete a
// source with listings — or a seeker with alerts — cannot be deleted at all
// ("record is part of a required relation reference"), which makes the UI's
// delete buttons fail as soon as anything has been polled.
//
// Also adds sources.seeded: the first poll of a new source ingests its
// backlog without alerting, so adding a feed can't fire dozens of
// notifications for listings that were already there.
func init() {
	migrations.Register(func(app core.App) error {
		cascades := map[string][]string{
			"listings": {"source"},
			"alerts":   {"user", "seeker", "listing"},
		}
		for name, fields := range cascades {
			col, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			for _, fieldName := range fields {
				rel, ok := col.Fields.GetByName(fieldName).(*core.RelationField)
				if !ok {
					return fmt.Errorf("%s.%s is not a relation field", name, fieldName)
				}
				rel.CascadeDelete = true
			}
			if err := app.Save(col); err != nil {
				return err
			}
		}

		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sources.Fields.Add(&core.BoolField{Name: "seeded"})
		if err := app.Save(sources); err != nil {
			return err
		}

		// Sources that already have listings have effectively been seeded;
		// marking them keeps their next poll alerting as before instead of
		// swallowing one round of genuinely new listings.
		existing, err := app.FindRecordsByFilter("sources", "", "", 0, 0)
		if err != nil {
			return err
		}
		for _, src := range existing {
			count, err := app.CountRecords("listings", dbx.HashExp{"source": src.Id})
			if err != nil {
				return err
			}
			if count > 0 {
				src.Set("seeded", true)
				if err := app.Save(src); err != nil {
					return err
				}
			}
		}
		return nil
	}, func(app core.App) error {
		cascades := map[string][]string{
			"listings": {"source"},
			"alerts":   {"user", "seeker", "listing"},
		}
		for name, fields := range cascades {
			col, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			for _, fieldName := range fields {
				if rel, ok := col.Fields.GetByName(fieldName).(*core.RelationField); ok {
					rel.CascadeDelete = false
				}
			}
			if err := app.Save(col); err != nil {
				return err
			}
		}

		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sources.Fields.RemoveByName("seeded")
		return app.Save(sources)
	}, "1700000001_cascade_and_seed.go")
}
