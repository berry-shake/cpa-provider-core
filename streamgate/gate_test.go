package streamgate

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
)

func TestClassifyStreamEvent(t *testing.T) {
	cases := []struct {
		protocol provider.Protocol
		event    string
		want     Kind
	}{
		{provider.Chat, `data: {"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, Prelude},
		{provider.Chat, `data: {"choices":[{"delta":{"content":"Hi"}}]}`, Content},
		{provider.Chat, `data: {"choices":[{"delta":{"reasoning_content":"…"}}]}`, Content},
		{provider.Chat, `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, Content},
		{provider.Chat, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, Terminal},
		{provider.Chat, `data: {"choices":[],"usage":{"total_tokens":3}}`, Prelude},
		{provider.Chat, `data: [DONE]`, Terminal},
		{provider.Chat, `data: {"error":{"code":"rate_limited","message":"slow down"}}`, Failed},
		{provider.Chat, `data: not json`, Content},
		{provider.Responses, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"error\":null}}", Prelude},
		{provider.Responses, `data: {"type":"response.output_item.added"}`, Prelude},
		{provider.Responses, `data: {"type":"response.output_text.delta","delta":"Hi"}`, Content},
		{provider.Responses, `data: {"type":"response.completed","response":{"error":null}}`, Terminal},
		{provider.Responses, `data: {"type":"response.incomplete"}`, Terminal},
		{provider.Responses, `data: {"type":"response.failed","response":{"error":{"code":"server_error"}}}`, Failed},
		{provider.Responses, `data: {"type":"error","code":"rate_limit_exceeded","message":"x"}`, Failed},
		{provider.Messages, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}", Prelude},
		{provider.Messages, "event: ping\ndata: {\"type\": \"ping\"}", Prelude},
		{provider.Messages, `data: {"type":"content_block_start","content_block":{"type":"text","text":""}}`, Prelude},
		{provider.Messages, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`, Content},
		{provider.Messages, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, Terminal},
		{provider.Messages, `data: {"type":"message_delta","delta":{"stop_reason":null}}`, Content},
		{provider.Messages, `data: {"type":"message_stop"}`, Terminal},
		{provider.Messages, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}", Failed},
		{provider.Messages, `event: message_start`, Prelude},
	}
	for _, tc := range cases {
		if got, _ := classifyStreamEvent(tc.protocol, NewEvent([]byte(tc.event+"\n\n"))); got != tc.want {
			t.Errorf("%s %q = %d, want %d", tc.protocol, tc.event, got, tc.want)
		}
	}
}

func TestStreamErrorStatus(t *testing.T) {
	for body, want := range map[string]int{
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`:     529,
		`{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`:              http.StatusTooManyRequests,
		`{"error":{"code":"rate_limited","message":"x"}}`:                                 http.StatusTooManyRequests,
		`{"type":"error","code":"rate_limit_exceeded","message":"x"}`:                     http.StatusTooManyRequests,
		`{"error":{"code":"insufficient_balance"}}`:                                       http.StatusPaymentRequired,
		`{"error":{"code":"request_blocked","type":"invalid_request_error"}}`:             http.StatusForbidden,
		`{"error":{"type":"invalid_request_error","message":"prompt is too long"}}`:       http.StatusBadRequest,
		`{"error":{"code":"upstream_timeout"}}`:                                           http.StatusGatewayTimeout,
		`{"error":{"code":"server_error","message":"x"}}`:                                 http.StatusBadGateway,
		`{"error":{"code":429,"message":"x"}}`:                                            http.StatusTooManyRequests,
		`{"error":{"message":"x","status":503}}`:                                          http.StatusServiceUnavailable,
		`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`: http.StatusBadGateway,
		`not json`: http.StatusBadGateway,
	} {
		if got := streamErrorStatus([]byte(body)); got != want {
			t.Errorf("streamErrorStatus(%s) = %d, want %d", body, got, want)
		}
	}
}

// The table also has the words OpenCode and OpenDesign report, for every
// provider.
func TestStreamErrorStatusProviderWords(t *testing.T) {
	for body, want := range map[string]int{
		`{"error":{"type":"budget_exceeded"}}`:       http.StatusTooManyRequests,
		`{"error":{"code":"insufficient_funds"}}`:    http.StatusPaymentRequired,
		`{"error":{"type":"AuthError"}}`:             http.StatusUnauthorized,
		`{"error":{"type":"MissingSessionID"}}`:      http.StatusBadRequest,
		`{"error":{"code":"rate_limit","type":"x"}}`: http.StatusTooManyRequests,
	} {
		if got := streamErrorStatus([]byte(body)); got != want {
			t.Errorf("streamErrorStatus(%s) = %d, want %d", body, got, want)
		}
	}
}

// Frames count the events that carry output, the same way in every
// protocol: deltas, not the events that close a part of the response.
func TestOutput(t *testing.T) {
	for _, tc := range []struct {
		protocol provider.Protocol
		event    string
		want     bool
	}{
		{provider.Messages, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`, true},
		{provider.Messages, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`, true},
		{provider.Messages, `{"type":"content_block_stop","index":0}`, false},
		{provider.Messages, `{"type":"message_delta","usage":{"output_tokens":3}}`, false},
		{provider.Responses, `{"type":"response.output_text.delta","delta":"a"}`, true},
		{provider.Responses, `{"type":"response.reasoning_summary_text.delta","delta":"a"}`, true},
		{provider.Responses, `{"type":"response.function_call_arguments.delta","delta":"{"}`, true},
		{provider.Responses, `{"type":"response.output_text.done","text":"a"}`, false},
		{provider.Responses, `{"type":"response.content_part.done"}`, false},
		{provider.Responses, `{"type":"response.output_item.done"}`, false},
		{provider.Chat, `{"choices":[{"delta":{"content":"a"}}]}`, true},
		{provider.Messages, `not json`, true},
	} {
		if got := Output(tc.protocol, NewEvent([]byte("data: "+tc.event+"\n\n"))); got != tc.want {
			t.Errorf("%s %s = %v", tc.protocol, tc.event, got)
		}
	}
	if !Output(provider.Messages, nil) {
		t.Error("raw chunk is not output")
	}
	// The event name stands in for a missing type member.
	if Output(provider.Messages, NewEvent([]byte("event: content_block_stop\ndata: {\"index\":0}\n\n"))) {
		t.Error("named content_block_stop counted")
	}
}

// testConfig is a gate config with a test error prefix.
func testConfig(protocol provider.Protocol) Config {
	return Config{Protocol: protocol, Errors: upstream.Errors{Prefix: "test: "}}
}

func runGate(t *testing.T, protocol provider.Protocol, events ...string) ([]string, *Gate) {
	t.Helper()
	var emitted []string
	gate := New(testConfig(protocol), true, http.Header{}, func(payload []byte) error {
		emitted = append(emitted, string(payload))
		return nil
	})
	for _, event := range events {
		if gate.Push([]byte(event)) != nil {
			break
		}
	}
	return emitted, gate
}

func TestGateHoldsPreludeUntilOutput(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	ping := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	delta := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	emitted, gate := runGate(t, provider.Messages, start, ping)
	if len(emitted) != 0 {
		t.Fatalf("prelude was not held: %q", emitted)
	}
	// A stream that stops before any output is truncated; the prelude is dropped.
	if failure := gate.Finish(context.Background(), nil); failure == nil || failure.Code != "stream_truncated" {
		t.Fatalf("prelude-only stream = %+v", failure)
	}

	emitted, gate = runGate(t, provider.Messages, start, ping, delta, stop, "data: [DONE]\n\n")
	if strings.Join(emitted, "") != start+ping+delta+stop {
		t.Fatalf("emitted %q", emitted)
	}
	if gate.Finish(context.Background(), nil) != nil {
		t.Fatal("complete stream reported an error")
	}

	// Chat completions keep [DONE].
	emitted, _ = runGate(t, provider.Chat, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n", "data: [DONE]\n\n")
	if len(emitted) != 2 || emitted[1] != "data: [DONE]\n\n" {
		t.Fatalf("chat emitted %q", emitted)
	}
}

func TestGatePreludeCap(t *testing.T) {
	ping := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" + strings.Repeat("x", 1000) + "\"}\n\n"
	events := make([]string, maxPreludeBytes/len(ping)+2)
	for i := range events {
		events[i] = ping
	}
	emitted, gate := runGate(t, provider.Messages, events...)
	if len(emitted) != len(events) || !gate.open {
		t.Fatalf("emitted %d of %d events past the cap", len(emitted), len(events))
	}
}

func TestGateEarlyError(t *testing.T) {
	emitted, gate := runGate(t, provider.Messages,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	failure := gate.Finish(context.Background(), errStreamRejected)
	if len(emitted) != 0 || failure == nil || failure.HTTPStatus != http.StatusServiceUnavailable || failure.Code != "overloaded_error" || !failure.Retryable {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	if !strings.Contains(failure.Message, "Overloaded") {
		t.Fatalf("message = %q", failure.Message)
	}

	_, gate = runGate(t, provider.Responses,
		"data: {\"type\":\"response.created\"}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}}}\n\n",
	)
	if failure = gate.Finish(context.Background(), errStreamRejected); failure == nil || failure.HTTPStatus != http.StatusTooManyRequests || failure.Code != "rate_limit_exceeded" {
		t.Fatalf("response.failed = %+v", failure)
	}
}

// An error after output reaches the client as is and the stream counts as
// ended, but StreamError reports it for the request log.
func TestGateErrorAfterOutputPassesThrough(t *testing.T) {
	delta := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	failed := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	emitted, gate := runGate(t, provider.Messages, delta, failed)
	if len(emitted) != 2 || emitted[1] != failed || gate.Finish(context.Background(), nil) != nil {
		t.Fatalf("emitted %q", emitted)
	}
	if late := gate.StreamError(); late == nil || late.Code != "overloaded_error" || late.HTTPStatus != http.StatusServiceUnavailable || late.Message != "test: Overloaded" {
		t.Fatalf("StreamError = %+v", late)
	}
	if gate.Phase() != "stream" {
		t.Fatalf("phase = %q", gate.Phase())
	}

	// A complete stream has none.
	_, gate = runGate(t, provider.Messages, delta, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if gate.Finish(context.Background(), nil) != nil || gate.StreamError() != nil {
		t.Fatalf("complete stream: %+v", gate.StreamError())
	}
}

func TestGateTruncated(t *testing.T) {
	emitted, gate := runGate(t, provider.Chat, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
	failure := gate.Finish(context.Background(), nil)
	if len(emitted) != 1 || failure == nil || failure.Code != "stream_truncated" || failure.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	// A finish reason is enough even without [DONE].
	_, gate = runGate(t, provider.Chat, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	if failure = gate.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("finished chat = %+v", failure)
	}
	// Raw bodies cannot be checked and end normally.
	raw := New(testConfig(provider.Chat), false, http.Header{}, func([]byte) error { return nil })
	_ = raw.Push([]byte(`{"partial":`))
	if failure = raw.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("raw = %+v", failure)
	}
}

// testFix holds back events named "hold" until the next event, renames
// events named "rename" and ends a stream that stops after output with the
// events it was given.
type testFix struct {
	held    []byte
	closing [][]byte
	flushed int
}

func (f *testFix) Event(event []byte) [][]byte {
	var out [][]byte
	if f.held != nil {
		out, f.held = append(out, f.held), nil
	}
	switch NewEvent(event).Name() {
	case "hold":
		f.held = event
		return out
	case "rename":
		event = []byte(strings.Replace(string(event), "event: rename", "event: renamed", 1))
	}
	return append(out, event)
}

func (f *testFix) Flush() [][]byte {
	f.flushed++
	if f.held == nil {
		return nil
	}
	held := f.held
	f.held = nil
	return [][]byte{held}
}

func (f *testFix) Close() [][]byte { return f.closing }

func TestGateFix(t *testing.T) {
	delta := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	hold := "event: hold\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"held\"}}\n\n"
	rename := "event: rename\ndata: {\"type\":\"ping\"}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var emitted []string
	var kinds []Kind
	run := func(fix *testFix, events ...string) *Gate {
		emitted, kinds = nil, nil
		config := testConfig(provider.Messages)
		config.Fix = fix
		gate := New(config, true, http.Header{}, func(payload []byte) error {
			emitted = append(emitted, string(payload))
			return nil
		})
		gate.Tap = func(kind Kind, _ *Event) { kinds = append(kinds, kind) }
		for _, event := range events {
			if gate.Push([]byte(event)) != nil {
				break
			}
		}
		return gate
	}

	// The gate reads what the fix returns: a held event comes out before
	// the next one, a renamed one under its new name.
	gate := run(&testFix{}, delta, hold, rename, stop)
	want := []string{delta, hold, strings.Replace(rename, "rename", "renamed", 1), stop}
	if strings.Join(emitted, "|") != strings.Join(want, "|") || gate.Finish(context.Background(), nil) != nil {
		t.Fatalf("emitted %q", emitted)
	}

	// A held event left at the end is flushed as is; with nothing to close
	// the stream the stream is truncated.
	fix := &testFix{}
	gate = run(fix, delta, hold)
	if failure := gate.Finish(context.Background(), nil); failure == nil || failure.Code != "stream_truncated" {
		t.Fatalf("held at the end = %+v", failure)
	}
	if len(emitted) != 2 || emitted[1] != hold || fix.flushed != 1 {
		t.Fatalf("emitted %q, flushed %d", emitted, fix.flushed)
	}

	// Closing events end the stream normally and go through the fix and the
	// tap like upstream events.
	fix = &testFix{closing: [][]byte{[]byte(rename), []byte(stop)}}
	gate = run(fix, delta)
	if failure := gate.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("closed stream = %+v", failure)
	}
	if len(emitted) != 3 || !strings.HasPrefix(emitted[1], "event: renamed") || kinds[len(kinds)-1] != Terminal {
		t.Fatalf("emitted %q, kinds %v", emitted, kinds)
	}

	// No closing before output, after a pump error or on a raw body.
	fix = &testFix{closing: [][]byte{[]byte(stop)}}
	gate = run(fix, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	if failure := gate.Finish(context.Background(), nil); failure == nil || failure.Code != "stream_truncated" {
		t.Fatalf("prelude only = %+v", failure)
	}
	fix = &testFix{closing: [][]byte{[]byte(stop)}}
	gate = run(fix, delta, hold)
	if failure := gate.Finish(context.Background(), errors.New("boom")); failure == nil || failure.Code != "upstream_stream_error" || fix.flushed != 0 {
		t.Fatalf("pump error = %+v, flushed %d", failure, fix.flushed)
	}
	config := testConfig(provider.Messages)
	config.Fix = &testFix{closing: [][]byte{[]byte(stop)}}
	raw := New(config, false, http.Header{}, func([]byte) error { return nil })
	_ = raw.Push([]byte(`{"partial":`))
	if failure := raw.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("raw = %+v", failure)
	}
}

func TestEventText(t *testing.T) {
	ev := NewEvent([]byte("event: x\ndata: {\"type\":\"a\\u0062\",\"n\":1}"))
	if ev.Text("type") != "ab" || ev.Text("n") != "" || ev.Text("missing") != "" {
		t.Fatalf("Text = %q %q", ev.Text("type"), ev.Text("n"))
	}
}
