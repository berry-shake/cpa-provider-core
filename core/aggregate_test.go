package core

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// chatOnlyAdapter is the test provider serving Chat Completions only, with
// its non-stream calls aggregated (ClinePass-shaped).
type chatOnlyAdapter struct{ *testAdapter }

func (a chatOnlyAdapter) Spec() provider.Spec {
	spec := a.testAdapter.Spec()
	spec.Protocols = []provider.Protocol{provider.Chat}
	spec.AggregateChat = true
	return spec
}

func newChatOnlyService(t *testing.T, upstream *fakeWokey) *Service {
	t.Helper()
	host := newFakeHost(t)
	svc := New(chatOnlyAdapter{newTestAdapter()}, "test", host.call)
	host.svc = svc
	if upstream != nil {
		svc.cfg.BaseURL = upstream.URL
	}
	svc.reqlog.Configure("", 10, 0)
	t.Cleanup(svc.Close)
	return svc
}

func sse(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("data: " + event + "\n\n")
	}
	return b.String()
}

func writeSSE(rw http.ResponseWriter, body string) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(body))
}

// aggregateOf feeds a stream to an aggregate the way the gate passes it on.
func aggregateOf(t *testing.T, body string) *chatAggregate {
	t.Helper()
	agg := newChatAggregate()
	for _, event := range strings.SplitAfter(body, "\n\n") {
		if strings.TrimSpace(event) == "" {
			continue
		}
		if err := agg.add([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	return agg
}

func TestChatAggregateJoinsAndMerges(t *testing.T) {
	agg := aggregateOf(t, sse(
		`{"id":"c1","object":"chat.completion.chunk","created":7,"model":"vendor/m","provider":"p1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"think "}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"hard","content":"Hel"}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"}},{"index":1,"delta":{"content":"other"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"b","arguments":""}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":1}"}},{"index":1,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"annotations":[{"a":1}]},"logprobs":{"content":[{"t":"x"}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"annotations":[{"a":2}]},"finish_reason":"tool_calls","logprobs":{"content":[{"t":"y"}]}},{"index":1,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`,
		`[DONE]`,
	))
	raw, perr := agg.result("wokey: ")
	if perr != nil {
		t.Fatal(perr)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["object"] != "chat.completion" || got["id"] != "c1" || got["model"] != "vendor/m" || got["provider"] != "p1" || got["created"] != 7.0 {
		t.Fatalf("top level = %s", raw)
	}
	choices := got["choices"].([]any)
	first := choices[0].(map[string]any)
	message := first["message"].(map[string]any)
	if message["role"] != "assistant" || message["content"] != "Hello" || message["reasoning_content"] != "think hard" || first["finish_reason"] != "tool_calls" {
		t.Fatalf("choice 0 = %s", raw)
	}
	tools := message["tool_calls"].([]any)
	a, b := tools[0].(map[string]any), tools[1].(map[string]any)
	if a["id"] != "call_a" || a["type"] != "function" || a["function"].(map[string]any)["name"] != "read" || a["function"].(map[string]any)["arguments"] != `{"path":1}` {
		t.Fatalf("tool 0 = %v", a)
	}
	if b["id"] != "call_b" || b["function"].(map[string]any)["name"] != "b" || b["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("tool 1 = %v", b)
	}
	if annotations := message["annotations"].([]any); len(annotations) != 2 {
		t.Fatalf("annotations = %v", annotations)
	}
	if logprobs := first["logprobs"].(map[string]any)["content"].([]any); len(logprobs) != 2 {
		t.Fatalf("logprobs = %v", logprobs)
	}
	second := choices[1].(map[string]any)
	if second["index"] != 1.0 || second["message"].(map[string]any)["content"] != "other" || second["finish_reason"] != "stop" {
		t.Fatalf("choice 1 = %v", second)
	}
	usage := got["usage"].(map[string]any)
	if usage["prompt_tokens"] != 10.0 || usage["completion_tokens"] != 5.0 || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != 4.0 {
		t.Fatalf("usage = %v", usage)
	}
}

func TestChatAggregateToolCallsOnlyHasNullContent(t *testing.T) {
	agg := aggregateOf(t, sse(
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	))
	raw, perr := agg.result("")
	if perr != nil {
		t.Fatal(perr)
	}
	if !strings.Contains(string(raw), `"content":null`) {
		t.Fatalf("result = %s", raw)
	}
}

func TestChatAggregateRefusesEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"no choices": sse(`{"id":"c1","choices":[],"usage":{"prompt_tokens":3}}`, `[DONE]`),
		"no output":  sse(`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`),
	} {
		if _, perr := aggregateOf(t, body).result("wokey: "); perr == nil || perr.Code != "empty_response" || !perr.Retryable || perr.HTTPStatus != http.StatusBadGateway || !strings.HasPrefix(perr.Message, "wokey: ") {
			t.Errorf("%s: %+v", name, perr)
		}
	}
}

func TestChatOnlyProviderDeclaresAndRefuses(t *testing.T) {
	svc := newChatOnlyService(t, nil)
	config := "models-refresh-minutes: 0\nbase-url: http://127.0.0.1:1\ndata-dir: " + strconv.Quote(t.TempDir()) + "\n"
	raw, _ := json.Marshal(rpcLifecycleRequest{ConfigYAML: []byte(config), SchemaVersion: 99})
	reg := mustDecode[rpcRegistration](t, decodeEnvelope(t, svc.Call(pluginabi.MethodPluginRegister, raw)).Result)
	if in, out := reg.Capabilities.ExecutorInputFormats, reg.Capabilities.ExecutorOutputFormats; len(in) != 1 || in[0] != "chat-completions" || len(out) != 1 || out[0] != "chat-completions" {
		t.Fatalf("formats = %v / %v", in, out)
	}
	for _, format := range []string{"claude", "openai-response"} {
		envelope := decodeEnvelope(t, svc.execute(executorRequest(format, "wokey/model-a", `{"model":"wokey/model-a"}`)))
		if envelope.OK || envelope.Error.Code != "unsupported_format" {
			t.Fatalf("%s: %+v", format, envelope)
		}
	}
	// The default still declares all three.
	full, _ := newTestService(t, nil)
	if all := mustDecode[rpcRegistration](t, decodeEnvelope(t, full.Call(pluginabi.MethodPluginRegister, raw)).Result).Capabilities.ExecutorInputFormats; len(all) != 3 {
		t.Fatalf("default formats = %v", all)
	}
}

func TestAggregatedCallSendsAStreamAndAnswersJSON(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(endpointChat, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("X-Request-Id", "req-9")
		writeSSE(rw, sse(
			`{"id":"c1","object":"chat.completion.chunk","model":"served/m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"served/m","choices":[{"index":0,"delta":{"content":"po"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"served/m","choices":[{"index":0,"delta":{"content":"ng"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"served/m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"served/m","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
			`[DONE]`,
		))
	}))
	svc := newChatOnlyService(t, upstream)

	envelope := decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","stream":false,"messages":[{"role":"user","content":"hi"}]}`)))
	if !envelope.OK {
		t.Fatalf("execute failed: %+v", envelope.Error)
	}
	resp := mustDecode[pluginapi.ExecutorResponse](t, envelope.Result)
	var body map[string]any
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatalf("payload %s: %v", resp.Payload, err)
	}
	message := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if body["object"] != "chat.completion" || message["content"] != "pong" || body["usage"].(map[string]any)["completion_tokens"] != 3.0 {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/json" || resp.Headers.Get("X-Request-Id") != "req-9" {
		t.Fatalf("headers = %v", resp.Headers)
	}
	got := receive(t, seen)
	options, _ := got.Body["stream_options"].(map[string]any)
	if got.Body["stream"] != true || options["include_usage"] != true || got.Header.Get("Accept") != "text/event-stream" || got.Body["model"] != "model-a" {
		t.Fatalf("upstream saw %+v", got)
	}
	items := svc.reqlog.Query(requestlog.Query{}).Items
	if len(items) != 1 {
		t.Fatalf("log = %+v", items)
	}
	e := items[0]
	if e.Status != "success" || e.Stream || e.InputTokens != 12 || e.OutputTokens != 3 || e.FinishReason != "stop" || e.ServedModel != "served/m" || e.TTFTMS == nil || e.Frames != nil {
		t.Fatalf("log entry = %+v", e)
	}
}

func TestAggregatedCallFailures(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"error before output", sse(`{"error":{"message":"slow down","code":429}}`), http.StatusTooManyRequests, ""},
		{"error after output", sse(`{"choices":[{"index":0,"delta":{"content":"par"}}]}`, `{"error":{"message":"provider failed","code":502}}`), 0, ""},
		{"truncated", sse(`{"choices":[{"index":0,"delta":{"content":"par"}}]}`), 0, ""},
		{"empty", sse(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`), http.StatusBadGateway, "empty_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newFakeWokey(t)
			upstream.handle(endpointChat, func(rw http.ResponseWriter, _ *http.Request) { writeSSE(rw, tc.body) })
			svc := newChatOnlyService(t, upstream)
			envelope := decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","messages":[]}`)))
			if envelope.OK {
				t.Fatalf("succeeded: %s", envelope.Result)
			}
			t.Logf("error = %+v", *envelope.Error)
			if tc.status != 0 && envelope.Error.HTTPStatus != tc.status || tc.code != "" && envelope.Error.Code != tc.code {
				t.Fatalf("error = %+v", envelope.Error)
			}
			items := svc.reqlog.Query(requestlog.Query{}).Items
			if len(items) != 1 || items[0].Status != "error" {
				t.Fatalf("log = %+v", items)
			}
		})
	}
}

func TestAggregatedCallAcceptsAPlainCompletion(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(endpointChat, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1}})
	})
	svc := newChatOnlyService(t, upstream)
	envelope := decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","messages":[]}`)))
	if !envelope.OK || !strings.Contains(string(mustDecode[pluginapi.ExecutorResponse](t, envelope.Result).Payload), `"content":"ok"`) {
		t.Fatalf("envelope = %+v", envelope)
	}

	// An error object instead of a stream fails the call.
	errUpstream := newFakeWokey(t)
	errUpstream.handle(endpointChat, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"success": false, "error": map[string]any{"message": "plan exhausted"}})
	})
	svc = newChatOnlyService(t, errUpstream)
	envelope = decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","messages":[]}`)))
	if envelope.OK || !strings.Contains(envelope.Error.Message, "plan exhausted") {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestAggregationOnlyForNonStreamChat(t *testing.T) {
	svc := newChatOnlyService(t, nil)
	for _, tc := range []struct {
		stream bool
		to     target
		want   bool
	}{
		{false, targetOf(endpointChat), true},
		{true, targetOf(endpointChat), false},
		{false, target{protocol: provider.Chat, endpoint: endpointChat, countTokens: true}, false},
	} {
		req := executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","stream":false,"messages":[]}`)
		req.Stream = tc.stream
		call, perr := svc.prepareCall(req, tc.to)
		if perr != nil {
			t.Fatal(perr)
		}
		if call.aggregate != tc.want || strings.Contains(string(call.body), `"stream":true`) != tc.want {
			t.Errorf("stream=%v countTokens=%v: aggregate=%v body=%s", tc.stream, tc.to.countTokens, call.aggregate, call.body)
		}
	}
	// Providers without AggregateChat send non-stream calls as they are.
	plain, _ := newTestService(t, nil)
	call, perr := plain.prepareCall(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","stream":false}`), targetOf(endpointChat))
	if perr != nil || call.aggregate || strings.Contains(string(call.body), `"stream":true`) || call.header.Get("Accept") != "application/json" {
		t.Fatalf("plain call = %+v %v", call, perr)
	}
}
