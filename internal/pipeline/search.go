package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/match"
)

// SearchScan bounds how many recent listings Search evaluates conditions
// against: conditions run in Go (attrs paths, loose typing), not SQL, so
// the scan has to stop somewhere.
const SearchScan = 5000

// Query selects stored listings the way a seeker would: by kind ("" or
// "any" for every kind), a source allow-list (empty for all) and
// conditions, optionally only those stored at or after Since.
type Query struct {
	Kind       string
	Sources    []string
	Since      time.Time
	Conditions []match.Condition
}

// Hit is one matching listing with its evaluation.
type Hit struct {
	Record *core.Record
	Result match.Result
}

// SearchResult is what Search found: Matched counts every match among the
// Scanned listings, Hits holds the newest `limit` of them.
type SearchResult struct {
	Scanned int
	Matched int
	Hits    []Hit
}

// Search evaluates q against stored listings, newest first. It is how
// seekers are tried out before (or while) they are saved: the web UI's
// live preview and MCP search_listings. Conditions must already be valid
// (match.Validate).
func Search(app core.App, q Query, limit int) (SearchResult, error) {
	filters, params := []string{}, dbx.Params{}
	if q.Kind != "" && q.Kind != "any" {
		filters, params["kind"] = append(filters, "kind = {:kind}"), q.Kind
	}
	if len(q.Sources) > 0 {
		or := make([]string, len(q.Sources))
		for i, id := range q.Sources {
			key := fmt.Sprintf("src%d", i)
			or[i], params[key] = "source = {:"+key+"}", id
		}
		filters = append(filters, "("+strings.Join(or, " || ")+")")
	}
	if !q.Since.IsZero() {
		filters, params["since"] = append(filters, "created >= {:since}"), q.Since.UTC().Format("2006-01-02 15:04:05.000Z")
	}
	recs, err := app.FindRecordsByFilter("listings", strings.Join(filters, " && "), "-created", SearchScan, 0, params)
	if err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Scanned: len(recs), Hits: []Hit{}}
	for _, r := range recs {
		res := match.Evaluate(q.Conditions, r)
		if !res.Matched {
			continue
		}
		out.Matched++
		if len(out.Hits) < limit {
			out.Hits = append(out.Hits, Hit{Record: r, Result: res})
		}
	}
	return out, nil
}
