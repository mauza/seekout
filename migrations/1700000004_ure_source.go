package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// Adds the "ure" (utahrealestate.com) source type to the sources.type
// select so the web UI can offer it alongside rss.
func init() {
	migrations.Register(func(app core.App) error {
		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sel, ok := sources.Fields.GetByName("type").(*core.SelectField)
		if !ok {
			return nil
		}
		for _, v := range sel.Values {
			if v == "ure" {
				return nil
			}
		}
		sel.Values = append(sel.Values, "ure")
		return app.Save(sources)
	}, func(app core.App) error {
		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sel, ok := sources.Fields.GetByName("type").(*core.SelectField)
		if !ok {
			return nil
		}
		for i, v := range sel.Values {
			if v == "ure" {
				sel.Values = append(sel.Values[:i], sel.Values[i+1:]...)
				break
			}
		}
		return app.Save(sources)
	}, "1700000004_ure_source.go")
}
