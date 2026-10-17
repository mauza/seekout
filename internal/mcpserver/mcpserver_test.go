package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	"github.com/mauza/seekout/internal/hooks"
	_ "github.com/mauza/seekout/migrations"
)

// bearer adds a fixed Authorization header, the way Hermes's headers config does.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func newApp(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	hooks.Bind(app)
	if err := hooks.SyncSelects(app); err != nil {
		t.Fatal(err)
	}
	return app
}

func save(t *testing.T, app core.App, collection string, fields map[string]any) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	for k, v := range fields {
		rec.Set(k, v)
	}
	if err := app.Save(rec); err != nil {
		t.Fatalf("save %s: %v", collection, err)
	}
	return rec
}

func newUser(t *testing.T, app core.App, email string, verified bool) (*core.Record, string) {
	t.Helper()
	u := save(t, app, "users", map[string]any{"email": email, "password": "password123", "verified": verified})
	token, err := u.NewStaticAuthToken(0)
	if err != nil {
		t.Fatal(err)
	}
	return u, token
}

func connect(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
}

// call runs a tool and decodes its structured result into out, returning the
// tool-level error text (empty on success).
func call(t *testing.T, s *mcp.ClientSession, name string, args any, out any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return strings.Join(msgs, "; ")
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
	}
	return ""
}

func TestMCP(t *testing.T) {
	app := newApp(t)
	srv := httptest.NewServer(Handler(app, "test"))
	defer srv.Close()

	alice, aliceToken := newUser(t, app, "alice@example.test", true)
	bob, bobToken := newUser(t, app, "bob@example.test", true)
	_, unverifiedToken := newUser(t, app, "eve@example.test", false)

	src := save(t, app, "sources", map[string]any{
		"name": "test-feed", "type": "rss", "kind": "home", "config": map[string]any{"url": "https://example.test/feed"},
		"interval_minutes": 30, "enabled": true,
	})
	for _, l := range []struct {
		id    string
		price float64
		beds  int
	}{{"a", 300000, 2}, {"b", 450000, 4}, {"c", 900000, 5}} {
		save(t, app, "listings", map[string]any{
			"source": src.Id, "external_id": l.id, "kind": "home", "title": "house " + l.id,
			"price": l.price, "attrs": map[string]any{"beds": l.beds},
		})
	}
	aliceAlerter := save(t, app, "alerters", map[string]any{"user": alice.Id, "name": "a", "type": "ntfy", "config": map[string]any{"topic": "a"}, "enabled": true})
	bobAlerter := save(t, app, "alerters", map[string]any{"user": bob.Id, "name": "b", "type": "ntfy", "config": map[string]any{"topic": "b"}, "enabled": true})

	t.Run("rejects missing, bad and unverified tokens", func(t *testing.T) {
		for _, tok := range []string{"", "not-a-jwt", unverifiedToken} {
			if s, err := connect(t, srv.URL, tok); err == nil {
				s.Close()
				t.Errorf("token %.10q: connected, want auth failure", tok)
			}
		}
	})

	as, err := connect(t, srv.URL, aliceToken)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	bs, err := connect(t, srv.URL, bobToken)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	t.Run("search applies seeker conditions", func(t *testing.T) {
		var out listingsOut
		msg := call(t, as, "search_listings", map[string]any{
			"kind": "home",
			"conditions": []map[string]any{
				{"field": "price", "op": "<", "value": 500000},
				{"field": "beds", "op": ">=", "value": 3},
			},
		}, &out)
		if msg != "" {
			t.Fatal(msg)
		}
		if len(out.Listings) != 1 || out.Listings[0].Title != "house b" {
			t.Fatalf("got %+v, want only house b", out.Listings)
		}
		if msg := call(t, as, "search_listings", map[string]any{
			"conditions": []map[string]any{{"field": "price", "op": "~", "value": 1}},
		}, nil); !strings.Contains(msg, "op") {
			t.Fatalf("bad op: got %q, want a validation error", msg)
		}
	})

	var seeker Seeker
	t.Run("create seeker owned by caller", func(t *testing.T) {
		if msg := call(t, as, "create_seeker", map[string]any{
			"name": "cheap", "source_ids": []string{src.Id}, "alerter_ids": []string{aliceAlerter.Id},
			"conditions": []map[string]any{{"field": "price", "op": "<", "value": 400000}},
		}, &seeker); msg != "" {
			t.Fatal(msg)
		}
		rec, err := app.FindRecordById("seekers", seeker.ID)
		if err != nil || rec.GetString("user") != alice.Id || rec.GetString("kind") != "any" || !rec.GetBool("enabled") {
			t.Fatalf("stored seeker wrong: %v %v", rec, err)
		}
	})

	t.Run("cannot attach another user's alerter", func(t *testing.T) {
		if msg := call(t, as, "create_seeker", map[string]any{
			"name": "sneaky", "alerter_ids": []string{bobAlerter.Id},
		}, nil); !strings.Contains(msg, "not found") {
			t.Fatalf("got %q, want not found", msg)
		}
	})

	t.Run("users are isolated", func(t *testing.T) {
		var seekers seekersOut
		call(t, bs, "list_seekers", map[string]any{}, &seekers)
		if len(seekers.Seekers) != 0 {
			t.Fatalf("bob sees %+v", seekers.Seekers)
		}
		for tool, args := range map[string]map[string]any{
			"update_seeker": {"id": seeker.ID, "name": "pwned"},
			"delete_seeker": {"id": seeker.ID},
		} {
			if msg := call(t, bs, tool, args, nil); !strings.Contains(msg, "not found") {
				t.Fatalf("%s on alice's seeker: got %q, want not found", tool, msg)
			}
		}
		var alerters alertersOut
		call(t, bs, "list_alerters", map[string]any{}, &alerters)
		if len(alerters.Alerters) != 1 || alerters.Alerters[0].ID != bobAlerter.Id {
			t.Fatalf("bob's alerters: %+v", alerters.Alerters)
		}
	})

	t.Run("update keeps omitted fields", func(t *testing.T) {
		var got Seeker
		if msg := call(t, as, "update_seeker", map[string]any{"id": seeker.ID, "enabled": false}, &got); msg != "" {
			t.Fatal(msg)
		}
		if got.Enabled || got.Name != "cheap" || len(got.Conditions) != 1 || len(got.Alerters) != 1 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if msg := call(t, as, "delete_seeker", map[string]any{"id": seeker.ID}, nil); msg != "" {
			t.Fatal(msg)
		}
		if _, err := app.FindRecordById("seekers", seeker.ID); err == nil {
			t.Fatal("seeker still exists")
		}
	})

	t.Run("only admins add sources", func(t *testing.T) {
		args := map[string]any{"name": "n", "type": "rss", "kind": "home", "config": map[string]any{"url": "https://example.test/f"}}
		if msg := call(t, as, "create_source", args, nil); !strings.Contains(msg, "only admins") {
			t.Fatalf("non-admin create_source: got %q, want refused", msg)
		}
		alice.Set("admin", true)
		if err := app.Save(alice); err != nil {
			t.Fatal(err)
		}
		var src Source
		if msg := call(t, as, "create_source", args, &src); msg != "" || src.ID == "" {
			t.Fatalf("admin create_source: %q %+v", msg, src)
		}
	})
}
