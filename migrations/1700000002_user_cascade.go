package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// Same class of bug as 1700000001 for the remaining owner relations: a user
// who owns a seeker or an alerter could not be deleted at all ("record is
// part of a required relation reference"). Per-user configuration is
// meaningless without its user, so it goes with them; sources and listings
// are shared and stay put.
func init() {
	migrations.Register(func(app core.App) error {
		return setUserCascade(app, true)
	}, func(app core.App) error {
		return setUserCascade(app, false)
	}, "1700000002_user_cascade.go")
}

func setUserCascade(app core.App, cascade bool) error {
	for _, name := range []string{"seekers", "alerters"} {
		col, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			return err
		}
		rel, ok := col.Fields.GetByName("user").(*core.RelationField)
		if !ok {
			return fmt.Errorf("%s.user is not a relation field", name)
		}
		rel.CascadeDelete = cascade
		if err := app.Save(col); err != nil {
			return err
		}
	}
	return nil
}
