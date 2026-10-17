package hooks

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/plugin"
	"github.com/mauza/seekout/internal/sources"
	_ "github.com/mauza/seekout/migrations"
)

type nopSource struct{}

func (nopSource) Poll(context.Context, *core.Record) ([]listing.Listing, error) { return nil, nil }

func newApp(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	Bind(app)
	return app
}

func record(t *testing.T, app core.App, collection string, fields map[string]any) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.Load(fields)
	return rec
}

// Registering a type is enough to save records of it: SyncSelects adds it
// to the select without a migration, and only once.
func TestSyncSelectsAddsRegisteredTypes(t *testing.T) {
	sources.Register(plugin.Info{Type: "brand-new", Label: "New"}, nopSource{})
	app := newApp(t)
	src := record(t, app, "sources", map[string]any{"name": "n", "type": "brand-new", "kind": "car", "interval_minutes": 30})
	if err := app.Save(src); err == nil {
		t.Fatal("saved an unsynced type; the select should still reject it")
	}
	for range 2 {
		if err := SyncSelects(app); err != nil {
			t.Fatal(err)
		}
	}
	// A record holds the collection it was made from, so make a new one.
	src = record(t, app, "sources", map[string]any{"name": "n", "type": "brand-new", "kind": "car", "interval_minutes": 30})
	if err := app.Save(src); err != nil {
		t.Fatalf("save after sync: %v", err)
	}
	col, _ := app.FindCollectionByNameOrId("sources")
	vals := col.Fields.GetByName("type").(*core.SelectField).Values
	if n := len(slices.DeleteFunc(slices.Clone(vals), func(v string) bool { return v != "brand-new" })); n != 1 {
		t.Errorf("type values = %v, want brand-new exactly once", vals)
	}
}

func TestConfigValidation(t *testing.T) {
	app := newApp(t)
	user := record(t, app, "users", map[string]any{"email": "u@example.test", "password": "password123"})
	if err := app.Save(user); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		collection string
		fields     map[string]any
		wantErr    string // "" = saves
	}{
		{"sources", map[string]any{"type": "rss", "config": map[string]any{}}, "url is required"},
		{"sources", map[string]any{"type": "rss", "config": map[string]any{"url": "file:///etc/passwd"}}, "http(s) URL"},
		{"sources", map[string]any{"type": "rss", "config": map[string]any{"url": "https://example.test/feed"}}, ""},
		{"sources", map[string]any{"type": "ure", "config": map[string]any{"pages": 9}}, "between 1 and 5"},
		{"sources", map[string]any{"type": "ure", "config": map[string]any{"type": "castle"}}, "must be one of"},
		{"sources", map[string]any{"type": "ure", "config": map[string]any{}}, ""},
		{"alerters", map[string]any{"type": "ntfy", "config": map[string]any{}}, "topic is required"},
		{"alerters", map[string]any{"type": "ntfy", "config": map[string]any{"topic": "t"}}, ""},
		// Undeclared keys are kept, not rejected.
		{"alerters", map[string]any{"type": "webhook", "config": map[string]any{
			"url": "https://example.test/hook", "headers": map[string]any{"X-Key": "k"}}}, ""},
	}
	for _, c := range cases {
		f := map[string]any{"name": "n", "kind": "home", "interval_minutes": 30, "user": user.Id}
		for k, v := range c.fields {
			f[k] = v
		}
		if c.collection == "alerters" {
			delete(f, "kind")
			delete(f, "interval_minutes")
		} else {
			delete(f, "user")
		}
		err := app.Save(record(t, app, c.collection, f))
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s %v: %v, want saved", c.collection, c.fields, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s %v: err = %v, want %q", c.collection, c.fields, err, c.wantErr)
		}
	}
}

func TestSeekerRules(t *testing.T) {
	app := newApp(t)
	var users []*core.Record
	for _, email := range []string{"a@example.test", "b@example.test"} {
		u := record(t, app, "users", map[string]any{"email": email, "password": "password123"})
		if err := app.Save(u); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	bobs := record(t, app, "alerters", map[string]any{"user": users[1].Id, "name": "b", "type": "ntfy", "config": map[string]any{"topic": "b"}})
	if err := app.Save(bobs); err != nil {
		t.Fatal(err)
	}
	seeker := func(extra map[string]any) error {
		f := map[string]any{"user": users[0].Id, "name": "s", "kind": "any"}
		for k, v := range extra {
			f[k] = v
		}
		return app.Save(record(t, app, "seekers", f))
	}
	if err := seeker(map[string]any{"alerters": []string{bobs.Id}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("another user's alerter: err = %v, want not found", err)
	}
	if err := seeker(map[string]any{"conditions": []map[string]any{{"field": "beds", "op": "~", "value": 3}}}); err == nil {
		t.Error("bad op saved")
	}
	if err := seeker(map[string]any{"conditions": []map[string]any{{"field": "beds", "op": ">=", "value": 3}}}); err != nil {
		t.Errorf("valid seeker: %v", err)
	}
}
