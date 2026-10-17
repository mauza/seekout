// Package plugin describes a source or alerter type: its name, what it is,
// and the config fields a record of that type takes. One declaration drives
// save-time config validation, the web UI's form (served from
// /api/seekout/plugins) and the MCP create_source docs, so adding a type is
// a Go file and nothing else.
package plugin

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Field types. Secret renders as a password input and is left out of
// summaries; the stored value is still plain JSON on the owner's record.
const (
	Text   = "text"
	URL    = "url"
	Number = "number"
	Select = "select"
	Secret = "secret"
)

// Field is one key of a record's config JSON.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"` // Select only
	Default     any      `json:"default,omitempty"` // what an omitted value means
	Min         *float64 `json:"min,omitempty"`     // Number only
	Max         *float64 `json:"max,omitempty"`     // Number only
	Placeholder string   `json:"placeholder,omitempty"`
	Hint        string   `json:"hint,omitempty"`
}

// Info describes one registered type.
type Info struct {
	Type        string  `json:"type"`        // the record's type value, e.g. "rss"
	Label       string  `json:"label"`       // short human name
	Description string  `json:"description"` // one line on what it does
	Fields      []Field `json:"fields"`
}

// Validate checks a record's config against the declared fields. Keys that
// aren't declared are allowed and kept: a type can take advanced settings
// (the webhook's headers) that the form doesn't offer.
func (i Info) Validate(config map[string]any) error {
	for _, f := range i.Fields {
		v, ok := config[f.Key]
		if !ok || v == nil || v == "" {
			if f.Required {
				return fmt.Errorf("%s is required", f.Key)
			}
			continue
		}
		switch f.Type {
		case Number:
			n, ok := v.(float64)
			if !ok {
				return fmt.Errorf("%s must be a number", f.Key)
			}
			if f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				return fmt.Errorf("%s must be between %s and %s", f.Key, bound(f.Min), bound(f.Max))
			}
		case Select:
			s, _ := v.(string)
			if !slices.Contains(f.Options, s) {
				return fmt.Errorf("%s must be one of %s", f.Key, strings.Join(f.Options, ", "))
			}
		case URL:
			s, _ := v.(string)
			if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return fmt.Errorf("%s must be an http(s) URL", f.Key)
			}
		default:
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be a string", f.Key)
			}
		}
	}
	return nil
}

// Doc renders the info for agent-facing tool descriptions, e.g.
// `ure (Utah MLS): config {type: residential|land|..., pages: number 1-5}`.
func (i Info) Doc() string {
	keys := make([]string, 0, len(i.Fields))
	for _, f := range i.Fields {
		k := f.Key + ": "
		switch {
		case f.Type == Select:
			k += strings.Join(f.Options, "|")
		case f.Type == Number && (f.Min != nil || f.Max != nil):
			k += "number " + bound(f.Min) + "-" + bound(f.Max)
		default:
			k += f.Type
		}
		if f.Required {
			k += " (required)"
		}
		keys = append(keys, k)
	}
	return fmt.Sprintf("%s (%s): config {%s}", i.Type, i.Label, strings.Join(keys, ", "))
}

func bound(p *float64) string {
	if p == nil {
		return "any"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// Float is a convenience for Field.Min and Field.Max.
func Float(v float64) *float64 { return &v }
