package core

// Non-stream Chat Completions calls of a provider with
// provider.Spec.AggregateChat go upstream as streams (prepareCall sets
// "stream": true) and are aggregated here into one chat.completion.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/streamgate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// errAggregateTooLarge stops a stream whose events outgrow maxResponseBytes.
var errAggregateTooLarge = errors.New("aggregated response too large")

// aggregateChat reads the upstream answer of an aggregated call (2xx) and
// answers the executor call with one chat.completion. The stream goes
// through a gate like any other, so the adapter's stream fix, the error and
// truncation rules and the request log apply; unlike a stream to a client,
// an error event after output fails the call too, since nothing has been
// delivered yet.
func (s *Service) aggregateChat(ctx context.Context, resp *http.Response, call *upstreamCall, rec *callRecord, fail func(*pluginabi.Error) []byte) []byte {
	headers := filterHeaders(resp.Header, forwardedResponseHeaders)
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	if !streamgate.IsEventStream(resp.Header.Get("Content-Type")) {
		return s.aggregateJSON(resp, headers, rec, fail)
	}
	agg := newChatAggregate()
	gate := streamgate.New(streamgate.Config{Protocol: provider.Chat, Errors: s.errors, Fix: call.fix}, true, resp.Header, agg.add)
	gate.Tap = rec.observe
	failure := gate.Finish(ctx, streamgate.Pump(resp.Body, true, gate.Push, func() {}))
	if errors.Is(agg.err, errAggregateTooLarge) {
		return fail(&pluginabi.Error{Code: "response_too_large", Message: fmt.Sprintf("%supstream response exceeds %d bytes", s.errors.Prefix, maxResponseBytes), HTTPStatus: http.StatusBadGateway})
	}
	if failure != nil {
		return fail(failure)
	}
	if late := gate.StreamError(); late != nil {
		return fail(late)
	}
	body, perr := agg.result(s.errors.Prefix)
	if perr != nil {
		return fail(perr)
	}
	rec.finish(nil, "")
	return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: headers})
}

// aggregateJSON takes an aggregated call answered with a plain body instead
// of a stream: a chat.completion is passed on, an error object fails the
// call like an HTTP error, anything else is a protocol failure.
func (s *Service) aggregateJSON(resp *http.Response, headers http.Header, rec *callRecord, fail func(*pluginabi.Error) []byte) []byte {
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if errRead != nil {
		return fail(s.errors.Transport(fmt.Errorf("read response: %w", errRead)))
	}
	var root map[string]any
	if json.Unmarshal(body, &root) == nil {
		if choices, _ := root["choices"].([]any); len(choices) > 0 && root["error"] == nil {
			rec.observeJSON(body)
			rec.finish(nil, "")
			return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: headers})
		}
		if root["error"] != nil || root["success"] == false {
			return fail(s.errors.Upstream(http.StatusBadGateway, resp.Header, body))
		}
	}
	return fail(&pluginabi.Error{
		Code:       "invalid_upstream_response",
		Message:    s.errors.Prefix + "expected an event stream, got " + firstNonEmpty(strings.TrimSpace(resp.Header.Get("Content-Type")), "no content type"),
		Retryable:  true,
		HTTPStatus: http.StatusBadGateway,
	})
}

// chatAggregate folds the chunks of a Chat Completions stream into one
// chat.completion: text fields of a delta are joined, tool calls merged by
// index (their function name and arguments joined), the last finish reason
// and usage kept, and the chunk's other top-level fields carried over.
type chatAggregate struct {
	root    map[string]any
	usage   map[string]any
	choices map[int]*aggregateChoice
	// output is set once a delta carried text, reasoning or a tool call.
	output bool
	size   int
	err    error
}

type aggregateChoice struct {
	message  map[string]any
	tools    map[int]map[string]any
	finish   any
	logprobs map[string]any
}

func newChatAggregate() *chatAggregate {
	return &chatAggregate{root: map[string]any{}, choices: map[int]*aggregateChoice{}}
}

// add takes one event as the gate passes it on.
func (a *chatAggregate) add(event []byte) error {
	if a.size += len(event); a.size > maxResponseBytes {
		a.err = errAggregateTooLarge
		return a.err
	}
	ev := streamgate.NewEvent(event)
	if string(ev.Data()) == "[DONE]" {
		return nil
	}
	if payload := ev.Payload(); payload != nil {
		a.observe(payload)
	}
	return nil
}

// textDeltas are the delta members whose fragments are joined.
var textDeltas = map[string]bool{"content": true, "reasoning_content": true, "reasoning": true, "refusal": true}

func (a *chatAggregate) observe(chunk map[string]any) {
	for key, value := range chunk {
		switch key {
		case "choices", "object", "obfuscation":
		case "usage":
			if usage, ok := value.(map[string]any); ok {
				if a.usage == nil {
					a.usage = map[string]any{}
				}
				mergeFields(a.usage, usage, false)
			}
		default:
			mergeFields(a.root, map[string]any{key: value}, false)
		}
	}
	items, _ := chunk["choices"].([]any)
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index := int(number(raw["index"]))
		choice := a.choices[index]
		if choice == nil {
			choice = &aggregateChoice{message: map[string]any{"role": "assistant"}, tools: map[int]map[string]any{}}
			a.choices[index] = choice
		}
		delta, _ := raw["delta"].(map[string]any)
		if delta == nil {
			// Some upstreams send a whole message in the last chunk.
			delta, _ = raw["message"].(map[string]any)
		}
		a.observeDelta(choice, delta)
		if finish := raw["finish_reason"]; finish != nil {
			choice.finish = finish
		}
		if logprobs, ok := raw["logprobs"].(map[string]any); ok {
			if choice.logprobs == nil {
				choice.logprobs = map[string]any{}
			}
			for key, value := range logprobs {
				if list, isList := value.([]any); isList {
					previous, _ := choice.logprobs[key].([]any)
					choice.logprobs[key] = append(previous, list...)
				} else if value != nil {
					choice.logprobs[key] = value
				}
			}
		}
	}
}

func (a *chatAggregate) observeDelta(choice *aggregateChoice, delta map[string]any) {
	message := choice.message
	for key, value := range delta {
		switch {
		case key == "role":
			if role, ok := value.(string); ok && role != "" {
				message["role"] = role
			}
		case textDeltas[key]:
			if text, ok := value.(string); ok && text != "" {
				appendText(message, key, text)
				a.output = true
			}
		case key == "tool_calls":
			calls, _ := value.([]any)
			for _, item := range calls {
				call, ok := item.(map[string]any)
				if !ok {
					continue
				}
				index := int(number(call["index"]))
				tool := choice.tools[index]
				if tool == nil {
					tool = map[string]any{}
					choice.tools[index] = tool
				}
				for field, part := range call {
					switch {
					case field == "index":
					case field == "function":
						if function, isMap := part.(map[string]any); isMap {
							inner, _ := tool["function"].(map[string]any)
							if inner == nil {
								inner = map[string]any{}
								tool["function"] = inner
							}
							mergeFields(inner, function, true)
						}
					case part != nil:
						tool[field] = part
					}
				}
				a.output = true
			}
		case key == "function_call":
			if function, ok := value.(map[string]any); ok {
				inner, _ := message[key].(map[string]any)
				if inner == nil {
					inner = map[string]any{}
					message[key] = inner
				}
				mergeFields(inner, function, true)
				a.output = true
			}
		default:
			switch typed := value.(type) {
			case nil:
			case map[string]any:
				inner, _ := message[key].(map[string]any)
				if inner == nil {
					inner = map[string]any{}
					message[key] = inner
				}
				mergeFields(inner, typed, false)
			case []any:
				previous, _ := message[key].([]any)
				message[key] = append(previous, typed...)
			default:
				message[key] = typed
			}
		}
	}
}

// result is the aggregated chat.completion; it fails a stream that carried
// no choice or no output at all.
func (a *chatAggregate) result(prefix string) ([]byte, *pluginabi.Error) {
	if len(a.choices) == 0 {
		return nil, &pluginabi.Error{Code: "empty_response", Message: prefix + "the upstream stream carried no completion", Retryable: true, HTTPStatus: http.StatusBadGateway}
	}
	if !a.output {
		// Empty answers are occasional (ClinePass); another try may succeed.
		return nil, &pluginabi.Error{Code: "empty_response", Message: prefix + "the upstream returned an empty completion", Retryable: true, HTTPStatus: http.StatusBadGateway}
	}
	out := make(map[string]any, len(a.root)+3)
	for key, value := range a.root {
		out[key] = value
	}
	out["object"] = "chat.completion"
	indices := make([]int, 0, len(a.choices))
	for index := range a.choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	choices := make([]any, 0, len(indices))
	for _, index := range indices {
		choice := a.choices[index]
		message := choice.message
		if len(choice.tools) > 0 {
			order := make([]int, 0, len(choice.tools))
			for toolIndex := range choice.tools {
				order = append(order, toolIndex)
			}
			sort.Ints(order)
			tools := make([]any, 0, len(order))
			for _, toolIndex := range order {
				tools = append(tools, choice.tools[toolIndex])
			}
			message["tool_calls"] = tools
		}
		if _, ok := message["content"]; !ok {
			message["content"] = nil
		}
		entry := map[string]any{"index": index, "message": message, "finish_reason": choice.finish}
		if choice.logprobs != nil {
			entry["logprobs"] = choice.logprobs
		}
		choices = append(choices, entry)
	}
	out["choices"] = choices
	if a.usage != nil {
		out["usage"] = a.usage
	}
	body, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		return nil, &pluginabi.Error{Code: "invalid_upstream_response", Message: prefix + "encode aggregated response: " + errMarshal.Error(), HTTPStatus: http.StatusBadGateway}
	}
	return body, nil
}

// textBuffer joins streamed string fragments; it encodes as the joined text.
type textBuffer struct{ text strings.Builder }

func (b *textBuffer) MarshalJSON() ([]byte, error) { return json.Marshal(b.text.String()) }

func appendText(dst map[string]any, key, fragment string) {
	buffer, ok := dst[key].(*textBuffer)
	if !ok {
		buffer = &textBuffer{}
		if previous, isString := dst[key].(string); isString {
			buffer.text.WriteString(previous)
		}
		dst[key] = buffer
	}
	buffer.text.WriteString(fragment)
}

// mergeFields merges src into dst: objects member by member, strings joined
// when concat is set and replaced otherwise, other values replaced; null
// leaves a value as it is.
func mergeFields(dst, src map[string]any, concat bool) {
	for key, value := range src {
		switch typed := value.(type) {
		case nil:
		case map[string]any:
			inner, _ := dst[key].(map[string]any)
			if inner == nil {
				inner = map[string]any{}
				dst[key] = inner
			}
			mergeFields(inner, typed, concat)
		case string:
			if concat {
				appendText(dst, key, typed)
			} else {
				dst[key] = typed
			}
		default:
			dst[key] = typed
		}
	}
}
