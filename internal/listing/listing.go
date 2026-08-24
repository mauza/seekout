// Package listing defines the normalized record every source produces.
package listing

import "time"

// Listing is the source-agnostic shape a poll produces. Kind is assigned by
// the pipeline from the source record, not by the source itself.
type Listing struct {
	ExternalID  string
	Title       string
	Price       float64
	URL         string
	Image       string
	Description string
	Published   time.Time
	Attrs       map[string]any
}
