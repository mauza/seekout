package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Admins and invite-only signup.
//
// Sources (and the listings they produce) are shared by every user, so
// changing them is now limited to users with admin = true. Grant it with
// `seekout admin <email>`; no API path can set it, since users could
// otherwise make themselves admin on signup or with a profile update.
//
// Signup needs a one-time invite code. The invites collection holds them
// (admins create, list and delete them); the check itself is a hook
// (internal/hooks), because a create rule can't claim the code atomically.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.Fields.Add(&core.BoolField{Name: "admin"})
		users.CreateRule = types.Pointer(`@request.body.admin:isset = false`)
		users.UpdateRule = types.Pointer(`id = @request.auth.id && @request.body.admin:isset = false`)
		if err := app.Save(users); err != nil {
			return err
		}

		admin := `@request.auth.admin = true`
		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sources.CreateRule = &admin
		sources.UpdateRule = &admin
		sources.DeleteRule = &admin
		if err := app.Save(sources); err != nil {
			return err
		}

		invites := core.NewBaseCollection("invites")
		invites.Fields.Add(
			&core.TextField{Name: "code", Required: true},
			&core.TextField{Name: "note"},
			&core.RelationField{Name: "created_by", CollectionId: users.Id},
			&core.RelationField{Name: "used_by", CollectionId: users.Id},
			&core.DateField{Name: "used_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		invites.AddIndex("idx_invites_code", true, "code", "")
		invites.ListRule = &admin
		invites.ViewRule = &admin
		invites.CreateRule = &admin
		invites.DeleteRule = &admin
		return app.Save(invites)
	}, func(app core.App) error {
		if invites, err := app.FindCollectionByNameOrId("invites"); err == nil {
			if err := app.Delete(invites); err != nil {
				return err
			}
		}
		loggedIn := `@request.auth.id != ""`
		sources, err := app.FindCollectionByNameOrId("sources")
		if err != nil {
			return err
		}
		sources.CreateRule = &loggedIn
		sources.UpdateRule = &loggedIn
		sources.DeleteRule = &loggedIn
		if err := app.Save(sources); err != nil {
			return err
		}
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.Fields.RemoveByName("admin")
		users.CreateRule = types.Pointer("")
		users.UpdateRule = types.Pointer("id = @request.auth.id")
		return app.Save(users)
	}, "1700000005_admin_invites.go")
}
