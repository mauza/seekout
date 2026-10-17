// Package listing defines the normalized record every source produces.
package listing

import "time"

// Kinds are the listing categories a source is filed under and a seeker
// can narrow to ("any" on a seeker means all of them). Adding one here is
// enough: boot adds it to the kind selects of sources, listings and seekers,
// and the web UI reads the list from /api/seekout/plugins.
var Kinds = []string{"home", "car", "classified", "other"}

// Listing is the source-agnostic shape a poll produces. Kind is assigned by
// the pipeline from the source record, not by the source itself.
type Listing struct {
	ExternalID  string // stable per listing within its source; the dedupe key
	Title       string
	Price       float64 // 0 when unknown
	URL         string
	Image       string
	Description string
	Published   time.Time
	// Attrs holds everything site-specific (beds, city, mileage, ...).
	// Seeker conditions address it by key or dot path, so keep keys
	// snake_case and values JSON-plain (string, number, bool, list, map).
	Attrs map[string]any
}
