package requestlog

import (
	"testing"
	"time"
)

func TestUsage(t *testing.T) {
	log := New("requests.db", "test: ", func(string) {})
	if _, errUsage := log.Usage("a1", "file-a", time.Now()); errUsage == "" {
		t.Fatal("a log that is off must say so")
	}
	log.Configure("", 1000, 0)
	defer log.Close()
	now := time.Unix(1_800_000_000, 0)
	add := func(ago time.Duration, index, id, status string, in, cached, out int64) {
		log.Add(Entry{Time: now.Add(-ago), Endpoint: "/v1/chat/completions", Model: "m", Status: status, AuthIndex: index, AuthID: id, InputTokens: in, CachedTokens: cached, OutputTokens: out, ReasoningTokens: out / 2})
	}
	add(time.Hour, "a1", "file-a", "success", 100, 40, 10)
	add(6*time.Hour, "a1", "file-a", "success", 300, 0, 30)
	add(2*time.Hour, "a1", "file-a", "error", 0, 0, 0)
	add(3*24*time.Hour, "a1", "file-a", "success", 1000, 0, 100)
	add(20*24*time.Hour, "", "file-a", "success", 5000, 1000, 500) // logged before the index was known
	add(40*24*time.Hour, "a1", "file-a", "success", 9, 9, 9)       // outside every window
	add(time.Hour, "b2", "file-b", "success", 7, 7, 7)             // another key

	usage, errUsage := log.Usage("a1", "file-a", now)
	if errUsage != "" {
		t.Fatal(errUsage)
	}
	want := Usage{
		FiveHours: UsageWindow{Requests: 2, Failed: 1, InputTokens: 100, CachedTokens: 40, OutputTokens: 10, ReasoningTokens: 5},
		Day:       UsageWindow{Requests: 3, Failed: 1, InputTokens: 400, CachedTokens: 40, OutputTokens: 40, ReasoningTokens: 20},
		Week:      UsageWindow{Requests: 4, Failed: 1, InputTokens: 1400, CachedTokens: 40, OutputTokens: 140, ReasoningTokens: 70},
		Month:     UsageWindow{Requests: 5, Failed: 1, InputTokens: 6400, CachedTokens: 1040, OutputTokens: 640, ReasoningTokens: 320},
	}
	if usage.FiveHours != want.FiveHours || usage.Day != want.Day || usage.Week != want.Week || usage.Month != want.Month {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	if usage.LogSince != now.Add(-40*24*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("log_since = %q", usage.LogSince)
	}
	if empty, _ := log.Usage("c3", "", now); empty.Month.Requests != 0 {
		t.Fatalf("unknown key usage = %+v", empty)
	}
}
