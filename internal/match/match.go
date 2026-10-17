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
	Op    string `json:"op"` // "=", "!=", ">", "<", ">=", "<=", "in", "contains"
	Value any    `json:"value"`

	// Soft conditions trade off against each other instead of each having
	// to hold. Value is the target, Floor the worst acceptable value (past
	// it the listing fails outright) and Best the value earning full credit.
	// See Evaluate for the scoring. Only ordering ops (> >= < <=) can be
	// soft, since they say which direction is better.
	Soft   bool     `json:"soft,omitempty" jsonschema:"trade this condition off against other soft ones instead of requiring it"`
	Floor  *float64 `json:"floor,omitempty" jsonschema:"soft only: worst acceptable value; past it the listing fails"`
	Best   *float64 `json:"best,omitempty" jsonschema:"soft only: value that earns the full +1"`
	Weight float64  `json:"weight,omitempty" jsonschema:"soft only: multiplier on this condition's score, default 1"` // 0 means 1
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
		if !c.Soft {
			continue
		}
		dir, ok := softDirection(c.Op)
		if !ok {
			return fmt.Errorf("condition %d (%s): only > >= < <= can be soft", i, c.Field)
		}
		t, ok := toFloat(c.Value)
		if !ok {
			return fmt.Errorf("condition %d (%s): a soft condition needs a numeric value", i, c.Field)
		}
		if c.Floor == nil || c.Best == nil {
			return fmt.Errorf("condition %d (%s): a soft condition needs floor and best", i, c.Field)
		}
		if dir*(t-*c.Floor) <= 0 {
			return fmt.Errorf("condition %d (%s): floor %v must be worse than the target %v", i, c.Field, *c.Floor, t)
		}
		if dir*(*c.Best-t) <= 0 {
			return fmt.Errorf("condition %d (%s): best %v must be better than the target %v", i, c.Field, *c.Best, t)
		}
		if c.Weight < 0 {
			return fmt.Errorf("condition %d (%s): weight must not be negative", i, c.Field)
		}
	}
	return nil
}

// Part is one soft condition's contribution to a Result.
type Part struct {
	Field  string  `json:"field"`
	Value  any     `json:"value"` // the listing's value, nil when missing
	Score  float64 `json:"score"` // -1..+1, before weighting
	Weight float64 `json:"weight"`
}

// Result is the outcome of evaluating a seeker against one listing.
type Result struct {
	Matched bool    `json:"matched"`
	Score   float64 `json:"score"` // weighted sum of Parts
	Parts   []Part  `json:"parts,omitempty"`
}

// Matches reports Evaluate(...).Matched.
func Matches(conditions []Condition, rec *core.Record) bool {
	return Evaluate(conditions, rec).Matched
}

// Evaluate checks a listing against a seeker. Hard conditions must all hold
// (logical AND); a seeker with no conditions matches everything from its
// sources. Each soft condition scores the listing's value against its
// target: 0 at the target, rising linearly to +1 at Best (capped), falling
// to -1 at Floor, and failing the listing outright past Floor. A missing or
// non-numeric value scores -1: unknown is treated as the worst acceptable,
// never as a pass. The listing matches when every hard condition holds, no
// soft one is past its floor, and the weighted sum of soft scores is >= 0,
// so a shortfall on one soft condition has to be paid for by others.
func Evaluate(conditions []Condition, rec *core.Record) Result {
	var attrs map[string]any
	_ = rec.UnmarshalJSONField("attrs", &attrs)

	res := Result{Matched: true}
	soft := false
	for _, c := range conditions {
		if c.Field == "" || c.Op == "" {
			return Result{}
		}
		lv, ok := fieldValue(rec, c.Field, attrs)
		if !c.Soft {
			if !ok || !compare(lv, c.Op, c.Value) {
				return Result{}
			}
			continue
		}
		soft = true
		score, pass := softScore(c, lv, ok)
		if !pass {
			return Result{}
		}
		w := c.Weight
		if w == 0 {
			w = 1
		}
		if !ok {
			lv = nil
		}
		res.Parts = append(res.Parts, Part{Field: c.Field, Value: lv, Score: score, Weight: w})
		res.Score += w * score
	}
	// The epsilon absorbs float error in sums that are exactly zero on paper.
	if soft && res.Score < -1e-9 {
		res.Matched = false
	}
	return res
}

// softDirection is +1 when higher values are better for op, -1 when lower.
func softDirection(op string) (float64, bool) {
	switch op {
	case ">", ">=":
		return 1, true
	case "<", "<=":
		return -1, true
	}
	return 0, false
}

// softScore scores one soft condition; pass is false past the floor or
// when the condition itself is malformed (Validate prevents that).
func softScore(c Condition, lv any, found bool) (score float64, pass bool) {
	dir, ok := softDirection(c.Op)
	t, tok := toFloat(c.Value)
	if !ok || !tok || c.Floor == nil || c.Best == nil {
		return 0, false
	}
	x, xok := toFloat(lv)
	if !found || !xok {
		return -1, true
	}
	gain := dir * (x - t)
	if gain >= 0 {
		return min(1, gain/(dir*(*c.Best-t))), true
	}
	span := dir * (t - *c.Floor)
	if -gain > span*(1+1e-12) {
		return 0, false
	}
	return max(-1, gain/span), true
}

// Explain renders a Result's soft parts for alert text, e.g.
// "score +0.15: garage_capacity 2 (-1.00 x2), lot_acres 0.43 (+1.00)".
func (r Result) Explain() string {
	if len(r.Parts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Parts))
	for _, p := range r.Parts {
		v := "missing"
		if p.Value != nil {
			v = stringify(p.Value)
		}
		w := ""
		if p.Weight != 1 {
			w = " x" + strconv.FormatFloat(p.Weight, 'f', -1, 64)
		}
		parts = append(parts, fmt.Sprintf("%s %s (%+.2f%s)", p.Field, v, p.Score, w))
	}
	return fmt.Sprintf("score %+.2f: %s", r.Score, strings.Join(parts, ", "))
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
