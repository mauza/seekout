// Package alerters delivers match notifications. An alerter type is one
// file: implement Alerter, describe its config with a plugin.Info, and
// Register both from init(). As with sources, registration also adds the
// type to the alerters.type select, validates saves, and lists it in the UI.
package alerters

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/mauza/seekout/internal/plugin"
)

// Alert is the notification payload handed to every alerter.
type Alert struct {
	Title string
	Body  string
	URL   string
	Image string
}

// Alerter sends one alert using the config stored on its record. Bound
// the send with its own timeout: a hung target must not stall a poll.
type Alerter interface {
	Send(ctx context.Context, rec *core.Record, alert Alert) error
}

type entry struct {
	info plugin.Info
	impl Alerter
}

var registry = map[string]entry{}

// Register wires an alerter type (info.Type, the alerters.type value) to
// its implementation. Called from init() in each alerter file.
func Register(info plugin.Info, a Alerter) {
	registry[info.Type] = entry{info, a}
}

// For resolves the implementation for an alerter record's type.
func For(rec *core.Record) (Alerter, error) {
	e, ok := registry[rec.GetString("type")]
	if !ok {
		return nil, fmt.Errorf("unknown alerter type %q", rec.GetString("type"))
	}
	return e.impl, nil
}

// Info returns the registered description of an alerter type.
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
