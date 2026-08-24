// Package alerters delivers match notifications. An alerter type is one
// file plus a Register call; alerter records pick one by name.
package alerters

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
)

// Alert is the notification payload handed to every alerter.
type Alert struct {
	Title string
	Body  string
	URL   string
	Image string
}

// Alerter sends one alert using the config stored on its record.
type Alerter interface {
	Send(ctx context.Context, rec *core.Record, alert Alert) error
}

var registry = map[string]Alerter{}

// Register wires an alerter type name (the alerters.type select value) to
// its implementation. Called from init() in each alerter file.
func Register(typ string, a Alerter) {
	registry[typ] = a
}

// For resolves the implementation for an alerter record's type.
func For(rec *core.Record) (Alerter, error) {
	typ := rec.GetString("type")
	a, ok := registry[typ]
	if !ok {
		return nil, fmt.Errorf("unknown alerter type %q", typ)
	}
	return a, nil
}
