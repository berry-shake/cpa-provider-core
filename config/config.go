// Package config reads the plugin-owned subtree under plugins.configs.<id> in
// the CPA config file. CPA hands the plugin only that subtree (plus its own
// enabled and priority keys, which are ignored here).
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultModelsRefreshMinutes  = 60
	defaultRequestTimeoutSeconds = 600
	defaultStreamIdleSeconds     = 300
	defaultLogRetention          = 100000
	defaultLogRetentionDays      = 30
	maxLogRetention              = 1000000
)

// Profile is what a provider sets about its config: the defaults that differ
// between the plugins, how base URLs are normalised and the descriptions of
// the provider-specific settings in CPA's plugin settings form.
type Profile struct {
	// BaseURL is the default base-url.
	BaseURL string
	// ModelPrefix is the default model-prefix (the provider ID).
	ModelPrefix string
	// DataDir is the default data-dir ("plugins/<id>-data").
	DataDir string
	// Database names the request log database in the data dir ("<id>.db").
	Database string
	// BaseURLSuffixes are dropped, in order and each at most once, from the
	// end of a base URL's path, so endpoint paths can be appended uniformly.
	// Every plugin drops "/v1"; Ollama also "/api", OpenCode "/openai",
	// "/anthropic" and "/google".
	BaseURLSuffixes []string
	// BaseURLHelp, ModelPrefixHelp and ModelsRefreshHelp describe base-url,
	// model-prefix and models-refresh-minutes.
	BaseURLHelp       string
	ModelPrefixHelp   string
	ModelsRefreshHelp string
}

// Config is the parsed plugin config.
type Config struct {
	// BaseURL is the upstream base URL; endpoint paths are appended.
	BaseURL string
	// ModelPrefix is the default CPA auth prefix for the provider's keys
	// ("ollama" -> "ollama/gpt-oss:120b"); CPA applies it with its native
	// prefix handling. Empty adds no prefix. An auth file's own prefix field
	// wins.
	ModelPrefix string
	// ModelsRefreshInterval is how often the model catalog is polled. Zero
	// disables polling; the catalog is still loaded on demand.
	ModelsRefreshInterval time.Duration
	// RequestTimeout bounds non-streaming upstream calls.
	RequestTimeout time.Duration
	// StreamIdleTimeout aborts a stream that has produced no bytes for this long.
	StreamIdleTimeout time.Duration
	// DataDir holds plugin data (the request log and the proxy list).
	// Relative paths resolve against CPA's working directory.
	DataDir string
	// LogRetention is how many requests the request log keeps; 0 disables it.
	LogRetention int
	// LogRetentionDays drops request log entries older than this many days;
	// 0 keeps them until LogRetention pushes them out.
	LogRetentionDays int

	// baseURLSuffixes is Profile.BaseURLSuffixes, for NormalizeBaseURL.
	baseURLSuffixes []string
}

type rawConfig struct {
	BaseURL               string  `yaml:"base-url"`
	ModelPrefix           *string `yaml:"model-prefix"`
	ModelsRefreshMinutes  *int    `yaml:"models-refresh-minutes"`
	RequestTimeoutSeconds *int    `yaml:"request-timeout-seconds"`
	StreamIdleSeconds     *int    `yaml:"stream-idle-timeout-seconds"`
	DataDir               string  `yaml:"data-dir"`
	LogRetention          *int    `yaml:"log-retention"`
	LogRetentionDays      *int    `yaml:"log-retention-days"`
}

// Default returns the config with every setting at its default.
func Default(p Profile) Config {
	return Config{
		BaseURL:               p.BaseURL,
		ModelPrefix:           p.ModelPrefix,
		ModelsRefreshInterval: defaultModelsRefreshMinutes * time.Minute,
		RequestTimeout:        defaultRequestTimeoutSeconds * time.Second,
		StreamIdleTimeout:     defaultStreamIdleSeconds * time.Second,
		DataDir:               p.DataDir,
		LogRetention:          defaultLogRetention,
		LogRetentionDays:      defaultLogRetentionDays,
		baseURLSuffixes:       p.BaseURLSuffixes,
	}
}

// Parse reads the plugin config subtree. Invalid values fall back to the
// defaults instead of failing registration, because a broken plugin config
// should not take every credential offline.
func Parse(raw []byte, p Profile) (Config, []string) {
	cfg := Default(p)
	var warnings []string
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	var parsed rawConfig
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return cfg, []string{"invalid plugin config, using defaults: " + errUnmarshal.Error()}
	}
	if base := strings.TrimSpace(parsed.BaseURL); base != "" {
		if normalized, errBase := cfg.NormalizeBaseURL(base); errBase != nil {
			warnings = append(warnings, "ignoring base-url: "+errBase.Error())
		} else {
			cfg.BaseURL = normalized
		}
	}
	if parsed.ModelPrefix != nil {
		cfg.ModelPrefix = NormalizePrefix(*parsed.ModelPrefix)
	}
	if parsed.ModelsRefreshMinutes != nil {
		if minutes := *parsed.ModelsRefreshMinutes; minutes >= 0 {
			cfg.ModelsRefreshInterval = time.Duration(minutes) * time.Minute
		} else {
			warnings = append(warnings, "ignoring negative models-refresh-minutes")
		}
	}
	if parsed.RequestTimeoutSeconds != nil {
		if seconds := *parsed.RequestTimeoutSeconds; seconds > 0 {
			cfg.RequestTimeout = time.Duration(seconds) * time.Second
		} else {
			warnings = append(warnings, "ignoring non-positive request-timeout-seconds")
		}
	}
	if parsed.StreamIdleSeconds != nil {
		if seconds := *parsed.StreamIdleSeconds; seconds > 0 {
			cfg.StreamIdleTimeout = time.Duration(seconds) * time.Second
		} else {
			warnings = append(warnings, "ignoring non-positive stream-idle-timeout-seconds")
		}
	}
	if dir := strings.TrimSpace(parsed.DataDir); dir != "" {
		cfg.DataDir = dir
	}
	if parsed.LogRetention != nil {
		if n := *parsed.LogRetention; n >= 0 {
			cfg.LogRetention = min(n, maxLogRetention)
		} else {
			warnings = append(warnings, "ignoring negative log-retention")
		}
	}
	if parsed.LogRetentionDays != nil {
		if n := *parsed.LogRetentionDays; n >= 0 {
			cfg.LogRetentionDays = min(n, 3650)
		} else {
			warnings = append(warnings, "ignoring negative log-retention-days")
		}
	}
	return cfg, warnings
}

// NormalizeBaseURL normalises a base URL with the suffixes of the profile
// the config was built from.
func (c Config) NormalizeBaseURL(raw string) (string, error) {
	return NormalizeBaseURL(raw, c.baseURLSuffixes)
}

// NormalizeBaseURL accepts an http(s) origin, optionally with a path prefix,
// and strips a trailing slash and then each of suffixes (in order, at most
// once each) so endpoint paths can be appended uniformly.
func NormalizeBaseURL(raw string, suffixes []string) (string, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("host is required")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	for _, suffix := range suffixes {
		parsed.Path = strings.TrimSuffix(parsed.Path, suffix)
	}
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// NormalizePrefix trims whitespace and surrounding slashes so "ollama/" and
// "/ollama" both mean "ollama".
func NormalizePrefix(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
}
