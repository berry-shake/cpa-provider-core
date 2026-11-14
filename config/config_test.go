package config

import (
	"reflect"
	"testing"
	"time"
)

// testProfile is Wokey's profile: one base URL suffix.
var testProfile = Profile{BaseURL: "https://api.wokey.ai", ModelPrefix: "wokey", DataDir: "plugins/wokey-data", Database: "wokey.db", BaseURLSuffixes: []string{"/v1"}}

func TestParseConfigDefaults(t *testing.T) {
	cfg, warnings := Parse(nil, testProfile)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if !reflect.DeepEqual(cfg, Default(testProfile)) {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
	if cfg.BaseURL != "https://api.wokey.ai" || cfg.ModelPrefix != "wokey" {
		t.Fatalf("unexpected defaults %+v", cfg)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 3
base-url: https://proxy.example.com/wokey/v1/
model-prefix: /wk/
models-refresh-minutes: 0
request-timeout-seconds: 30
stream-idle-timeout-seconds: 45
`)
	cfg, warnings := Parse(raw, testProfile)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if cfg.BaseURL != "https://proxy.example.com/wokey" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.ModelPrefix != "wk" {
		t.Errorf("ModelPrefix = %q", cfg.ModelPrefix)
	}
	if cfg.ModelsRefreshInterval != 0 {
		t.Errorf("ModelsRefreshInterval = %v", cfg.ModelsRefreshInterval)
	}
	if cfg.RequestTimeout != 30*time.Second || cfg.StreamIdleTimeout != 45*time.Second {
		t.Errorf("timeouts = %v / %v", cfg.RequestTimeout, cfg.StreamIdleTimeout)
	}
}

func TestParseConfigEmptyPrefixRegistersBareIDs(t *testing.T) {
	cfg, _ := Parse([]byte(`model-prefix: ""`), testProfile)
	if cfg.ModelPrefix != "" {
		t.Fatalf("ModelPrefix = %q, want empty", cfg.ModelPrefix)
	}
}

func TestParseConfigInvalidValuesFallBack(t *testing.T) {
	cfg, warnings := Parse([]byte(`
base-url: ftp://example.com
models-refresh-minutes: -1
request-timeout-seconds: 0
stream-idle-timeout-seconds: -5
`), testProfile)
	if len(warnings) != 4 {
		t.Fatalf("warnings = %v, want 4", warnings)
	}
	if !reflect.DeepEqual(cfg, Default(testProfile)) {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}

	cfg, warnings = Parse([]byte("base-url: [unterminated"), testProfile)
	if len(warnings) != 1 || !reflect.DeepEqual(cfg, Default(testProfile)) {
		t.Fatalf("broken YAML: cfg=%+v warnings=%v", cfg, warnings)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.wokey.ai":          "https://api.wokey.ai",
		"https://api.wokey.ai/":         "https://api.wokey.ai",
		"https://api.wokey.ai/v1":       "https://api.wokey.ai",
		"https://api.wokey.ai/v1/?x=1":  "https://api.wokey.ai",
		"http://127.0.0.1:8080/gw/v1/":  "http://127.0.0.1:8080/gw",
		" https://api.wokey.ai/v1#frag": "https://api.wokey.ai",
	}
	for in, want := range cases {
		got, errBase := NormalizeBaseURL(in, testProfile.BaseURLSuffixes)
		if errBase != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, errBase, want)
		}
	}
	for _, bad := range []string{"api.wokey.ai", "ftp://api.wokey.ai", "https://"} {
		if _, errBase := NormalizeBaseURL(bad, testProfile.BaseURLSuffixes); errBase == nil {
			t.Errorf("NormalizeBaseURL(%q) accepted", bad)
		}
	}
}

// Suffixes are dropped in order, each at most once: Ollama drops /v1 then
// /api, OpenCode /v1 and its protocol prefixes.
func TestNormalizeBaseURLSuffixes(t *testing.T) {
	cases := []struct {
		suffixes []string
		in, want string
	}{
		{[]string{"/v1", "/api"}, "https://ollama.com/api/", "https://ollama.com"},
		{[]string{"/v1", "/api"}, "https://ollama.com/v1", "https://ollama.com"},
		{[]string{"/v1", "/api"}, "https://ollama.com/api/v1", "https://ollama.com"},
		{[]string{"/v1", "/api"}, "https://ollama.com/v1/api", "https://ollama.com/v1"},
		{[]string{"/v1", "/openai", "/anthropic", "/google"}, "https://opencode.ai/inference/go/anthropic/v1", "https://opencode.ai/inference/go"},
		{[]string{"/v1", "/openai", "/anthropic", "/google"}, "https://opencode.ai/inference/go/openai", "https://opencode.ai/inference/go"},
	}
	for _, tc := range cases {
		if got, errBase := NormalizeBaseURL(tc.in, tc.suffixes); errBase != nil || got != tc.want {
			t.Errorf("NormalizeBaseURL(%q, %v) = %q, %v; want %q", tc.in, tc.suffixes, got, errBase, tc.want)
		}
	}
}
