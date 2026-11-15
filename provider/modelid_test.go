package provider

import "testing"

func TestUpstreamID(t *testing.T) {
	catalog := map[string]bool{"gpt-5": true, "vendor/model": true}
	has := func(id string) bool { return catalog[id] }
	cases := []struct {
		model, prefix string
		slashed, flat string
	}{
		// The credential's prefix is dropped, whatever follows it.
		{"wk/gpt-5", "wk", "gpt-5", "gpt-5"},
		{"wk/unknown", "wk", "unknown", "unknown"},
		{"wk/vendor/model", "wk", "vendor/model", "vendor/model"},
		// Catalog IDs pass, slashed ones included.
		{"gpt-5", "", "gpt-5", "gpt-5"},
		{"vendor/model", "wk", "vendor/model", "vendor/model"},
		{"  gpt-5  ", "", "gpt-5", "gpt-5"},
		// An unknown prefix before a catalog model is dropped.
		{"old/gpt-5", "wk", "gpt-5", "gpt-5"},
		// An unknown prefix before an unknown model: kept for slashed IDs,
		// dropped for flat ones.
		{"other/unknown", "wk", "other/unknown", "unknown"},
		{"a/b/c", "", "a/b/c", "c"},
		// A trailing slash leaves nothing to keep.
		{"other/", "", "other/", "other/"},
		// The reasoning suffix goes before the prefix rules.
		{"wk/gpt-5(high)", "wk", "gpt-5", "gpt-5"},
		{"gpt-5 (8192)", "", "gpt-5", "gpt-5"},
		{"old/gpt-5(low)", "wk", "gpt-5", "gpt-5"},
		// No prefix configured.
		{"unknown", "", "unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := UpstreamID(tc.model, tc.prefix, has, SlashedIDs); got != tc.slashed {
			t.Errorf("UpstreamID(%q, %q, SlashedIDs) = %q, want %q", tc.model, tc.prefix, got, tc.slashed)
		}
		if got := UpstreamID(tc.model, tc.prefix, has, FlatIDs); got != tc.flat {
			t.Errorf("UpstreamID(%q, %q, FlatIDs) = %q, want %q", tc.model, tc.prefix, got, tc.flat)
		}
	}
}

func TestStripThinkingSuffix(t *testing.T) {
	cases := map[string]string{
		"gpt-5(high)":   "gpt-5",
		"gpt-5 (8192)":  "gpt-5",
		"gpt-5":         "gpt-5",
		"(high)":        "(high)", // nothing before the suffix
		"gpt-5)":        "gpt-5)", // no opening parenthesis
		"a(b)c(d)":      "a(b)c",
		"vendor/m(low)": "vendor/m",
	}
	for model, want := range cases {
		if got := StripThinkingSuffix(model); got != want {
			t.Errorf("StripThinkingSuffix(%q) = %q, want %q", model, got, want)
		}
	}
}
