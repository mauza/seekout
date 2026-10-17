// Package match evaluates seeker conditions against listing records.
// Ported from the original oprah match_prop_fn with two fixes: the per-user
// cache bleed is gone (no cache — SQLite reads are cheap) and a missing field
// now fails only its own condition instead of erroring the whole match.
package match

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// Condition is one row of a seeker's conditions JSON array.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`   // "=", "!=", ">", "<", ">=", "<=", "in", "contains"
	Value any    `json:"value"`
}

// Ops lists the supported condition operators.
var Ops = []string{"=", "!=", ">", "<", ">=", "<=", "in", "contains"}

// Validate rejects conditions Matches would silently treat as never-true.
func Validate(conditions []Condition) error {
	for i, c := range conditions {
		if c.Field == "" {
			return fmt.Errorf("condition %d: field is required", i)
		}
		if !slices.Contains(Ops, c.Op) {
			return fmt.Errorf("condition %d: op %q is not one of %s", i, c.Op, strings.Join(Ops, " "))
		}
	}
	return nil
}

// Matches returns true when every condition holds for the listing (logical
// AND). A seeker with no conditions matches everything from its sources.
func Matches(conditions []Condition, rec *core.Record) bool {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)

	for _, c := range conditions {
		if c.Field == "" || c.Op == "" {
			return false
		}
		lv, ok := fieldValue(rec, c.Field, attrs)
		if !ok || !compare(lv, c.Op, c.Value) {
			return false
		}
	}
	return true
}

// fieldValue resolves a condition field: typed listing columns first, then a
// dot path into the attrs JSON (e.g. "beds", "address.city"). A leading
// "attrs." is optional and stripped, since that prefix reads naturally and
// earlier builds of the UI suggested fields in that form.
func fieldValue(rec *core.Record, field string, attrs map[string]any) (any, bool) {
	switch field {
	case "title":
		return rec.GetString("title"), true
	case "price":
		return rec.GetFloat("price"), true
	case "url":
		return rec.GetString("url"), true
	case "image":
		return rec.GetString("image"), true
	case "description":
		return rec.GetString("description"), true
	case "kind":
		return rec.GetString("kind"), true
	case "external_id":
		return rec.GetString("external_id"), true
	case "published":
		return rec.GetDateTime("published").String(), true
	}
	path := strings.TrimPrefix(field, "attrs.")
	cur := any(attrs)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func compare(lv any, op string, cv any) bool {
	switch op {
	case "=", "!=":
		eq := valuesEqual(lv, cv)
		return eq == (op == "=")
	case ">", "<", ">=", "<=":
		return ordered(lv, op, cv)
	case "in":
		return inSet(lv, cv)
	case "contains":
		return contains(lv, cv)
	}
	return false
}

// valuesEqual compares numerically when both sides parse as numbers, falling
// back to string comparison — same intent as the Python version's type cast.
func valuesEqual(lv, cv any) bool {
	if lf, lok := toFloat(lv); lok {
		if cf, cok := toFloat(cv); cok {
			return lf == cf
		}
	}
	return stringify(lv) == stringify(cv)
}

func ordered(lv any, op string, cv any) bool {
	if lf, lok := toFloat(lv); lok {
		if cf, cok := toFloat(cv); cok {
			switch op {
			case ">":
				return lf > cf
			case "<":
				return lf < cf
			case ">=":
				return lf >= cf
			case "<=":
				return lf <= cf
			}
		}
		return false
	}
	ls, cs := stringify(lv), stringify(cv)
	switch op {
	case ">":
		return ls > cs
	case "<":
		return ls < cs
	case ">=":
		return ls >= cs
	case "<=":
		return ls <= cs
	}
	return false
}

// inSet is the Python `in` operator: field value must be a member of the
// condition value (a JSON array or a comma-separated string).
func inSet(lv, cv any) bool {
	ls := stringify(lv)
	switch v := cv.(type) {
	case []any:
		for _, item := range v {
			if valuesEqual(lv, item) {
				return true
			}
		}
		return false
	case string:
		for part := range strings.SplitSeq(v, ",") {
			if strings.TrimSpace(part) == ls {
				return true
			}
		}
	}
	return false
}

// contains: substring for string fields, membership for list fields.
func contains(lv, cv any) bool {
	if list, ok := lv.([]any); ok {
		for _, item := range list {
			if valuesEqual(item, cv) {
				return true
			}
		}
		return false
	}
	return strings.Contains(stringify(lv), stringify(cv))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(n, ",", "")), 64)
		return f, err == nil
	}
	return 0, false
}

func stringify(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}
