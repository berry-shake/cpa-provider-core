package upstream

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamErrorMapping(t *testing.T) {
	retry := http.Header{"Retry-After": []string{"7"}}
	cases := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		want      int
		code      string
		retryable bool
		retryIn   int
	}{
		{"invalid key cools the key", 401, nil, `{"error":{"code":"invalid_api_key","message":"bad key"}}`, 401, "invalid_api_key", false, 0},
		{"no balance cools the key", 402, nil, `{"error":{"type":"insufficient_balance","message":"top up"}}`, 402, "insufficient_balance", false, 0},
		// Wokey turns this 403 into a 400 with Errors.Remap (TestRemap).
		{"blocked request keeps its status", 403, nil, `{"error":{"code":"request_blocked","message":"blocked"}}`, 403, "request_blocked", false, 0},
		{"account 403 keeps its status", 403, nil, `{"error":{"code":"key_disabled","message":"disabled"}}`, 403, "key_disabled", false, 0},
		{"unknown model does not cool the key", 404, nil, `{"error":{"code":"model_not_found","message":"no model"}}`, 400, "model_not_found", false, 0},
		{"rate limit keeps Retry-After", 429, retry, `{"code":"rate_limited","message":"slow down"}`, 429, "rate_limited", true, 7},
		{"overloaded is transient", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, 503, "overloaded_error", true, 0},
		{"aborted downstream", 499, nil, ``, 408, "upstream_499", true, 0},
		{"plain text body", 500, nil, `boom`, 500, "upstream_500", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := tc.header
			if header == nil {
				header = http.Header{}
			}
			err := Errors{Prefix: "test: "}.Upstream(tc.status, header, []byte(tc.body))
			if err.HTTPStatus != tc.want || err.Code != tc.code || err.Retryable != tc.retryable || err.RetryAfterSeconds != tc.retryIn {
				t.Fatalf("got %+v", err)
			}
			if !strings.HasPrefix(err.Message, "test: ") {
				t.Fatalf("message = %q", err.Message)
			}
		})
	}
}

// Remap sees the code and the untruncated message before the default
// mapping, may pick the status and rewrite the message; 0 keeps the default
// status.
func TestRemap(t *testing.T) {
	var seen []string
	errs := Errors{Prefix: "test: ", Remap: func(status int, code, message string) (int, string) {
		seen = append(seen, code)
		if status == http.StatusForbidden && code == "request_blocked" {
			return http.StatusBadRequest, message
		}
		if rest, ok := strings.CutPrefix(message, "[code="+code+"] "); ok {
			return 0, rest
		}
		return 0, message
	}}
	blocked := errs.Upstream(403, http.Header{}, []byte(`{"error":{"code":"request_blocked","message":"blocked"}}`))
	if blocked.HTTPStatus != 400 || blocked.Code != "request_blocked" || blocked.Message != "test: blocked" || blocked.Retryable {
		t.Fatalf("blocked = %+v", blocked)
	}
	if account := errs.Upstream(403, http.Header{}, []byte(`{"error":{"code":"key_disabled","message":"disabled"}}`)); account.HTTPStatus != 403 {
		t.Fatalf("account 403 = %+v", account)
	}
	long := strings.Repeat("x", MaxErrorBodyBytes)
	stripped := errs.Upstream(404, http.Header{}, []byte(`{"error":{"code":"model_not_found","message":"[code=model_not_found] `+long+`"}}`))
	if stripped.HTTPStatus != 400 || stripped.Message != "test: "+long {
		t.Fatalf("stripped = %d %q", stripped.HTTPStatus, stripped.Message[:40])
	}
	if plain := errs.Upstream(500, http.Header{}, []byte("boom")); plain.Message != "test: boom" || plain.Code != "upstream_500" {
		t.Fatalf("plain = %+v", plain)
	}
	if strings.Join(seen, ",") != "request_blocked,key_disabled,model_not_found," {
		t.Fatalf("seen = %q", seen)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := retryAfterSeconds("12", now); got != 12 {
		t.Errorf("delta = %d", got)
	}
	if got := retryAfterSeconds(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90 {
		t.Errorf("date = %d", got)
	}
	for _, raw := range []string{"", "-3", "soon", now.Add(-time.Minute).Format(http.TimeFormat)} {
		if got := retryAfterSeconds(raw, now); got != 0 {
			t.Errorf("retryAfterSeconds(%q) = %d", raw, got)
		}
	}
}
