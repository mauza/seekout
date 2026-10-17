// Package sources plugs site-specific pollers into the pipeline. A source
// type is one file: implement Source (and optionally Enricher), describe its
// config with a plugin.Info, and Register both from init(). Registration is
// all it takes: boot adds the type to the sources.type select, saves are
// validated against the Info, and the web UI and MCP tools list it.
package sources

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/plugin"
)

// Source polls one configured source record and returns normalized
// listings. Return everything the site currently shows (newest first is
// typical): the pipeline dedupes on (source, ExternalID), so ExternalID
// must be stable for the life of a listing.
type Source interface {
	Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error)
}

// Enricher is optionally implemented by a Source whose list results are
// thin (e.g. no garage or lot size): the pipeline calls Enrich only for
// listings it has not stored before, so a per-listing request is paid once.
// An error keeps the listing out of the database until a later poll
// enriches it, so fail rather than store a guess.
type Enricher interface {
	Enrich(ctx context.Context, src *core.Record, l *listing.Listing) error
}

type entry struct {
	info plugin.Info
	impl Source
}

var registry = map[string]entry{}

// Register wires a source type (info.Type, the sources.type value) to its
// implementation. Called from init() in each source file.
func Register(info plugin.Info, s Source) {
	registry[info.Type] = entry{info, s}
}

// For resolves the implementation for a source record's type.
func For(src *core.Record) (Source, error) {
	e, ok := registry[src.GetString("type")]
	if !ok {
		return nil, fmt.Errorf("unknown source type %q", src.GetString("type"))
	}
	return e.impl, nil
}

// Info returns the registered description of a source type.
func Info(typ string) (plugin.Info, bool) {
	e, ok := registry[typ]
	return e.info, ok
}

// Infos returns every registered type, sorted by name.
func Infos() []plugin.Info {
	out := make([]plugin.Info, 0, len(registry))
	for _, e := range registry {
		out = append(out, e.info)
	}
	slices.SortFunc(out, func(a, b plugin.Info) int { return strings.Compare(a.Type, b.Type) })
	return out
}
