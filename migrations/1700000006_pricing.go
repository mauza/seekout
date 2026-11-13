package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// Price tracking (internal/pricing).
//
//   - items: a price-comparison group the classifier files listings under,
//     priced per unit ("GB" for memory, so different capacities pool;
//     "each" for things that don't divide).
//   - price_observations: one priced line of a listing (most listings have
//     exactly one), with quantity, amount per piece and the unit price.
//   - price_refs: a web-research anchor for an item, with the verified
//     citations it was computed from.
//
// Logged-in users can read all three (listings are shared, and so is what
// they cost); only server code writes them, so they have no create, update
// or delete rules.
func init() {
	migrations.Register(func(app core.App) error {
		loggedIn := `@request.auth.id != ""`

		listings, err := app.FindCollectionByNameOrId("listings")
		if err != nil {
			return err
		}
		seekers, err := app.FindCollectionByNameOrId("seekers")
		if err != nil {
			return err
		}

		items := core.NewBaseCollection("items")
		items.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "name_key", Required: true},
			&core.TextField{Name: "category"},
			&core.TextField{Name: "kind"}, // listing kind it was created from; candidates stay within a kind
			&core.TextField{Name: "unit", Required: true},
			&core.TextField{Name: "description"},
			&core.DateField{Name: "last_observed"},
			&core.DateField{Name: "last_researched"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		items.AddIndex("idx_items_name_key", true, "name_key", "")
		items.ListRule = &loggedIn
		items.ViewRule = &loggedIn
		if err := app.Save(items); err != nil {
			return err
		}

		obs := core.NewBaseCollection("price_observations")
		obs.Fields.Add(
			&core.RelationField{Name: "listing", CollectionId: listings.Id, Required: true, CascadeDelete: true},
			&core.RelationField{Name: "item", CollectionId: items.Id, Required: true, CascadeDelete: true},
			&core.RelationField{Name: "seekers", CollectionId: seekers.Id, MaxSelect: 100},
			&core.TextField{Name: "label"},
			&core.NumberField{Name: "quantity"},
			&core.NumberField{Name: "amount"},
			&core.NumberField{Name: "total_price"},
			&core.NumberField{Name: "unit_price"},
			&core.TextField{Name: "condition"},
			&core.TextField{Name: "dedupe_key"},
			&core.DateField{Name: "observed_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		obs.AddIndex("idx_price_obs_item_time", false, "item, observed_at", "")
		obs.AddIndex("idx_price_obs_listing", false, "listing", "")
		obs.ListRule = &loggedIn
		obs.ViewRule = &loggedIn
		if err := app.Save(obs); err != nil {
			return err
		}

		refs := core.NewBaseCollection("price_refs")
		refs.Fields.Add(
			&core.RelationField{Name: "item", CollectionId: items.Id, Required: true, CascadeDelete: true},
			&core.TextField{Name: "status"}, // ok | no_data | error
			&core.NumberField{Name: "low"},
			&core.NumberField{Name: "typical"},
			&core.NumberField{Name: "high"},
			&core.NumberField{Name: "new_typical"},
			&core.NumberField{Name: "n_new"},
			&core.NumberField{Name: "n_used"},
			&core.NumberField{Name: "rejected"},
			&core.JSONField{Name: "citations"},
			&core.JSONField{Name: "queries"},
			&core.TextField{Name: "model"},
			&core.TextField{Name: "error"},
			&core.AutodateField{Name: "created", OnCreate: true},
		)
		refs.AddIndex("idx_price_refs_item_time", false, "item, created", "")
		refs.ListRule = &loggedIn
		refs.ViewRule = &loggedIn
		return app.Save(refs)
	}, func(app core.App) error {
		for _, name := range []string{"price_refs", "price_observations", "items"} {
			col, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if err := app.Delete(col); err != nil {
				return err
			}
		}
		return nil
	}, "1700000006_pricing.go")
}
