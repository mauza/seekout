// Package sources plugs site-specific pollers into the pipeline. A source
// type is one file plus a Register call; source records pick one by name.
package sources

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/core"

	"gitlab.mauza.net/mauza/seekout/internal/listing"
)

// Source polls one configured source record and returns normalized listings.
type Source interface {
	Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error)
}

// Enricher is optionally implemented by a Source whose list results are
// thin (e.g. no garage or lot size): the pipeline calls Enrich only for
// listings it has not stored before, so a per-listing request is paid once.
type Enricher interface {
	Enrich(ctx context.Context, src *core.Record, l *listing.Listing) error
}

var registry = map[string]Source{}

// Register wires a source type name (the sources.type select value) to its
// implementation. Called from init() in each source file.
func Register(typ string, s Source) {
	registry[typ] = s
}

// For resolves the implementation for a source record's type.
func For(src *core.Record) (Source, error) {
	typ := src.GetString("type")
	s, ok := registry[typ]
	if !ok {
		return nil, fmt.Errorf("unknown source type %q", typ)
	}
	return s, nil
}

// Types returns the registered type names (used for the collection schema).
func Types() []string {
	out := make([]string, 0, len(registry))
	for typ := range registry {
		out = append(out, typ)
	}
	return out
}
