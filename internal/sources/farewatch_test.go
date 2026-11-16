package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

const farewatchSample = `{"deals":[{
 "id":123,"created_at":"2026-11-16T13:05:00Z",
 "watch_id":"socal","watch_name":"Southern California",
 "origin":"SLC","destination":"SAN","destination_name":"San Diego International Airport",
 "outbound_date":"2026-12-27","return_date":"2027-01-03","nights":7,
 "price_total":1298,"price_per_person":259.6,"passengers":5,"currency":"USD",
 "airlines":["Frontier"],"stops":0,
 "depart_time":"2026-12-27 10:34","arrive_time":"2026-12-27 14:15","duration_minutes":161,
 "school_days_missed":0,"price_level":"low","typical_low":1500,"typical_high":2100,
 "baseline":1800,"pct_below":27.9,"reason":"27.9% below route median",
 "google_flights_url":"https://www.google.com/travel/flights?..."
 }]}`

func TestFarewatchPoll(t *testing.T) {
	for _, minimal := range []bool{false, true} {
		t.Run(map[bool]string{false: "sample", true: "optional fields"}[minimal], func(t *testing.T) {
			var body struct {
				Deals []map[string]any `json:"deals"`
			}
			if err := json.Unmarshal([]byte(farewatchSample), &body); err != nil {
				t.Fatal(err)
			}
			want := body.Deals[0]
			if minimal {
				for k := range want {
					switch k {
					case "id", "created_at", "origin", "destination", "outbound_date", "return_date", "price_total":
					case "airlines", "reason", "stops", "watch_id":
						want[k] = nil
					default:
						delete(want, k)
					}
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				days, auth, watch := "30", "Bearer secret", "socal"
				if minimal {
					days, auth, watch = "14", "", ""
				}
				if r.Method != "GET" || r.URL.Path != "/api/deals" || r.URL.Query().Get("days") != days || r.URL.Query().Get("limit") != "200" || r.URL.Query().Get("watch") != watch || r.Header.Get("Authorization") != auth {
					t.Errorf("request = %s %s, authorization %q", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			col := core.NewBaseCollection("sources")
			col.Fields.Add(&core.JSONField{Name: "config"})
			src := core.NewRecord(col)
			cfg := map[string]any{"base_url": srv.URL + "/"}
			if !minimal {
				cfg["days"], cfg["token"], cfg["watch"] = 30, "secret", "socal"
			}
			src.Set("config", cfg)
			got, err := (farewatchSource{}).Poll(context.Background(), src)
			if err != nil || len(got) != 1 {
				t.Fatalf("poll = %v, %v", got, err)
			}
			l := got[0]
			title := "SLC→SAN Dec 27–Jan 3 · $1,298 · Frontier nonstop"
			reason, url := want["reason"], want["google_flights_url"]
			if minimal {
				title, reason, url = "SLC→SAN Dec 27–Jan 3 · $1,298", "", ""
			}
			published, _ := time.Parse(time.RFC3339, "2026-11-16T13:05:00Z")
			if l.ExternalID != "deal-123" || l.Title != title || l.Price != 1298 || !l.Published.Equal(published) || l.Description != reason || l.URL != url {
				t.Errorf("listing = %+v", l)
			}
			for k, v := range want {
				if v == nil {
					delete(want, k)
				}
			}
			if !minimal {
				want["airline"] = "Frontier"
			}
			if !reflect.DeepEqual(l.Attrs, want) {
				t.Errorf("attrs = %#v, want %#v", l.Attrs, want)
			}
		})
	}
}

func TestFarewatchPollErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"non-200", `{"deals":[]}`, 503},
		{"missing deals", `{}`, 200},
		{"null deals", `{"deals":null}`, 200},
		{"wrong deals type", `{"deals":{}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			col := core.NewBaseCollection("sources")
			col.Fields.Add(&core.JSONField{Name: "config"})
			src := core.NewRecord(col)
			src.Set("config", map[string]any{"base_url": srv.URL})
			if _, err := (farewatchSource{}).Poll(context.Background(), src); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
