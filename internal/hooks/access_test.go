package hooks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// api serves app's REST API (collection rules and request hooks included)
// and returns a caller: call(method, path, token, body) -> status, response.
func api(t *testing.T, app core.App) func(string, string, string, any) (int, map[string]any) {
	t.Helper()
	r, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return func(method, path, token string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
}

func userToken(t *testing.T, app core.App, email string, admin bool) (*core.Record, string) {
	t.Helper()
	u := record(t, app, "users", map[string]any{"email": email, "password": "password123", "verified": true, "admin": admin})
	if err := app.Save(u); err != nil {
		t.Fatal(err)
	}
	tok, err := u.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	return u, tok
}

func TestInviteOnlySignup(t *testing.T) {
	app := newApp(t)
	call := api(t, app)
	admin, adminTok := userToken(t, app, "admin@example.test", true)
	_, userTok := userToken(t, app, "user@example.test", false)

	signup := func(email, invite string) (int, map[string]any) {
		body := map[string]any{"email": email, "password": "password123", "passwordConfirm": "password123"}
		if invite != "" {
			body["invite"] = invite
		}
		return call("POST", "/api/collections/users/records", "", body)
	}

	if st, _ := signup("a@example.test", ""); st != 400 {
		t.Errorf("signup without invite: %d, want 400", st)
	}
	if st, _ := signup("a@example.test", "nope"); st != 400 {
		t.Errorf("signup with a bad invite: %d, want 400", st)
	}
	if st, _ := call("POST", "/api/collections/invites/records", userTok, map[string]any{}); st != 400 && st != 403 {
		t.Errorf("non-admin created an invite: %d", st)
	}

	st, inv := call("POST", "/api/collections/invites/records", adminTok, map[string]any{"note": "for a friend", "code": "chosen"})
	code, _ := inv["code"].(string)
	if st != 200 || len(code) != 10 || code == "chosen" || inv["created_by"] != admin.Id {
		t.Fatalf("admin invite: %d %v, want a generated 10-char code stamped with the admin", st, inv)
	}

	st, user := signup("a@example.test", code)
	if st != 200 {
		t.Fatalf("signup with invite: %d %v", st, user)
	}
	got, err := app.FindFirstRecordByData("invites", "code", code)
	if err != nil || got.GetString("used_by") != user["id"] || got.GetDateTime("used_at").IsZero() {
		t.Errorf("invite not marked used: %v %v", got, err)
	}
	if st, _ := signup("b@example.test", code); st != 400 {
		t.Errorf("reused invite: %d, want 400", st)
	}

	// A failed signup must not spend the code.
	_, inv2 := call("POST", "/api/collections/invites/records", adminTok, map[string]any{})
	code2, _ := inv2["code"].(string)
	if st, _ := signup("a@example.test", code2); st != 400 { // duplicate email
		t.Errorf("duplicate-email signup: %d, want 400", st)
	}
	if st, _ := signup("c@example.test", code2); st != 200 {
		t.Errorf("invite spent by a failed signup: %d", st)
	}
}

func TestAdminOnlySources(t *testing.T) {
	app := newApp(t)
	call := api(t, app)
	_, adminTok := userToken(t, app, "admin@example.test", true)
	user, userTok := userToken(t, app, "user@example.test", false)

	src := map[string]any{"name": "s", "type": "rss", "kind": "home", "interval_minutes": 30,
		"config": map[string]any{"url": "https://example.test/feed"}}
	if st, _ := call("POST", "/api/collections/sources/records", userTok, src); st != 400 && st != 403 {
		t.Errorf("non-admin created a source: %d", st)
	}
	st, created := call("POST", "/api/collections/sources/records", adminTok, src)
	if st != 200 {
		t.Fatalf("admin create source: %d %v", st, created)
	}
	id, _ := created["id"].(string)
	if st, _ := call("PATCH", "/api/collections/sources/records/"+id, userTok, map[string]any{"enabled": false}); st != 404 && st != 403 {
		t.Errorf("non-admin updated a source: %d", st)
	}
	if st, _ := call("DELETE", "/api/collections/sources/records/"+id, userTok, nil); st != 404 && st != 403 {
		t.Errorf("non-admin deleted a source: %d", st)
	}
	if st, _ := call("GET", "/api/collections/sources/records/"+id, userTok, nil); st != 200 {
		t.Errorf("non-admin can't read a shared source: %d", st)
	}

	// Nobody can grant themselves admin through the API.
	if st, _ := call("PATCH", "/api/collections/users/records/"+user.Id, userTok, map[string]any{"admin": true}); st == 200 {
		t.Error("user made themselves admin via update")
	}
	_, inv := call("POST", "/api/collections/invites/records", adminTok, map[string]any{})
	if st, _ := call("POST", "/api/collections/users/records", "", map[string]any{
		"email": "x@example.test", "password": "password123", "passwordConfirm": "password123",
		"invite": inv["code"], "admin": true,
	}); st == 200 {
		t.Error("signup set admin")
	}
	if fresh, _ := app.FindRecordById("users", user.Id); fresh.GetBool("admin") {
		t.Error("user is admin")
	}
}
