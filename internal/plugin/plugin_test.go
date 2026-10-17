package plugin

import "testing"

func TestDoc(t *testing.T) {
	in := Info{Type: "ure", Label: "Utah MLS", Fields: []Field{
		{Key: "type", Type: Select, Options: []string{"residential", "land"}},
		{Key: "pages", Type: Number, Min: Float(1), Max: Float(5)},
		{Key: "url", Type: URL, Required: true},
	}}
	want := "ure (Utah MLS): config {type: residential|land, pages: number 1-5, url: url (required)}"
	if got := in.Doc(); got != want {
		t.Errorf("Doc() = %q\nwant    %q", got, want)
	}
}

func TestValidateTypes(t *testing.T) {
	in := Info{Type: "x", Fields: []Field{{Key: "n", Type: Number}, {Key: "s", Type: Text}}}
	for _, cfg := range []map[string]any{{"n": "3"}, {"s": 3.0}} {
		if err := in.Validate(cfg); err == nil {
			t.Errorf("Validate(%v) = nil, want a type error", cfg)
		}
	}
	if err := in.Validate(map[string]any{"n": 3.0, "s": "ok", "extra": true}); err != nil {
		t.Errorf("valid config: %v", err)
	}
}
