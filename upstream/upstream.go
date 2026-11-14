// Package upstream holds what the provider plugins share about talking to
// their upstream APIs: mapping an upstream error answer to the plugin error
// CPA sees, the transport, request and credential errors, a JSON GET with a
// bearer token and the number readers for loosely typed answers.
package upstream

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// MaxErrorBodyBytes bounds the upstream error text copied into a plugin error.
const MaxErrorBodyBytes = 2048

// Errors builds the plugin errors of one provider.
type Errors struct {
	// Prefix starts every message: the provider ID and ": " ("ollama: ").
	Prefix string
}

// Upstream converts a non-2xx upstream answer into a plugin error whose
// HTTP status drives CPA's credential cooldown:
//
//   - 401, 402 and 403 keep their status, so CPA cools the key down.
//   - 404 (unknown model) is a request problem, not a key problem, so it
//     becomes 400 and does not cool the key (a 404 would cool the model on
//     this key for 12 hours).
//   - 529 (overloaded) becomes 503 (transient); 499 (the caller went away)
//     becomes 408; anything outside 4xx and 5xx becomes 502.
//   - 429 keeps Retry-After so CPA waits exactly that long.
func (e Errors) Upstream(status int, header http.Header, body []byte) *pluginabi.Error {
	code, message := parseBody(body)
	mapped := status
	switch {
	case status == http.StatusNotFound:
		mapped = http.StatusBadRequest
	case status == 529:
		mapped = http.StatusServiceUnavailable
	case status == 499:
		// downstream_aborted: the caller went away; report it as a timeout-like
		// transient failure rather than a client error on a healthy key.
		mapped = http.StatusRequestTimeout
	case status < 400 || status > 599:
		mapped = http.StatusBadGateway
	}
	if code == "" {
		code = "upstream_" + strconv.Itoa(status)
	}
	if message == "" {
		message = Truncate(string(body), MaxErrorBodyBytes)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	err := &pluginabi.Error{
		Code:       code,
		Message:    e.Prefix + message,
		HTTPStatus: mapped,
		Retryable:  mapped == http.StatusTooManyRequests || mapped == http.StatusRequestTimeout || mapped >= 500,
	}
	if mapped == http.StatusTooManyRequests {
		err.RetryAfterSeconds = retryAfterSeconds(header.Get("Retry-After"), time.Now())
	}
	return err
}

// parseBody extracts a code and message from the error shapes the
// upstreams use: {"error":"message"}, OpenAI style
// {"error":{"code","type","message"}}, Anthropic style
// {"type":"error","error":{"type","message"}} and flat {"code","message"}.
func parseBody(body []byte) (string, string) {
	var parsed struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return "", ""
	}
	code := jsonfield.Text(parsed.Code)
	message := strings.TrimSpace(parsed.Message)
	switch inner := parsed.Error.(type) {
	case map[string]any:
		if value := jsonfield.Text(inner["code"]); value != "" {
			code = value
		} else if value := jsonfield.Text(inner["type"]); value != "" && code == "" {
			code = value
		}
		if value, ok := inner["message"].(string); ok && strings.TrimSpace(value) != "" {
			message = strings.TrimSpace(value)
		}
	case string:
		if message == "" {
			message = strings.TrimSpace(inner)
		}
	}
	return code, Truncate(message, MaxErrorBodyBytes)
}

// retryAfterSeconds parses Retry-After as delta seconds or an HTTP date.
func retryAfterSeconds(raw string, now time.Time) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, errAtoi := strconv.Atoi(raw); errAtoi == nil {
		if seconds < 0 {
			return 0
		}
		return seconds
	}
	if when, errParse := http.ParseTime(raw); errParse == nil {
		if delta := when.Sub(now); delta > 0 {
			return int(delta.Round(time.Second) / time.Second)
		}
	}
	return 0
}

// Transport reports a network failure reaching the upstream as a transient 502.
func (e Errors) Transport(err error) *pluginabi.Error {
	return &pluginabi.Error{
		Code:       "upstream_unreachable",
		Message:    e.Prefix + err.Error(),
		HTTPStatus: http.StatusBadGateway,
		Retryable:  true,
	}
}

// RequestError reports a request the plugin rejects before calling the upstream.
func RequestError(code, message string) *pluginabi.Error {
	return &pluginabi.Error{Code: code, Message: message, HTTPStatus: http.StatusBadRequest}
}

// AuthError reports an unusable credential, so CPA cools it down.
func AuthError(message string) *pluginabi.Error {
	return &pluginabi.Error{Code: "invalid_credential", Message: message, HTTPStatus: http.StatusUnauthorized}
}

// GetJSON GETs a JSON object with a bearer token. name labels the endpoint in
// errors; a non-200 answer goes through Upstream.
func (e Errors) GetJSON(ctx context.Context, client *http.Client, target, token, userAgent, name string) (map[string]any, *pluginabi.Error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errReq != nil {
		return nil, e.Transport(errReq)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, e.Transport(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, e.Transport(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, e.Upstream(resp.StatusCode, resp.Header, body)
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(body, &out); errUnmarshal != nil {
		return nil, &pluginabi.Error{Code: "invalid_upstream_response", Message: e.Prefix + "decode " + name + ": " + errUnmarshal.Error(), HTTPStatus: http.StatusBadGateway}
	}
	return out, nil
}

// Truncate trims text and cuts it to limit bytes, marking the cut.
func Truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// NumberField returns the first of keys that holds a number (see
// OptionalNumber), or 0.
func NumberField(fields map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := OptionalNumber(fields[key]); ok {
			return value
		}
	}
	return 0
}

// OptionalNumber accepts JSON numbers and decimal strings; null is absent.
func OptionalNumber(raw any) (float64, bool) {
	var parsed float64
	switch value := raw.(type) {
	case float64:
		parsed = value
	case string:
		number, errParse := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if errParse != nil {
			return 0, false
		}
		parsed = number
	case json.Number:
		number, errParse := value.Float64()
		if errParse != nil {
			return 0, false
		}
		parsed = number
	default:
		return 0, false
	}
	// ParseFloat reads "NaN" and "Infinity"; they are no amount, and JSON
	// cannot carry them to the page or CPA's quota panel.
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}
