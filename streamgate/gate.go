// Package streamgate reads upstream streaming responses: it splits SSE
// bodies into events and holds back the start of a stream until it is known
// not to be an error, so CPA can still retry it on another credential.
package streamgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// maxPreludeBytes caps how much of a stream is held back while waiting for
// the first output event.
const maxPreludeBytes = 64 << 10

// errStreamRejected stops the pump when the upstream reports an error inside
// the stream before any output was sent.
var errStreamRejected = errors.New("upstream rejected the request inside the stream")

// Kind classifies a stream event.
type Kind int

const (
	// Prelude carries no output yet (message_start, response.created, a
	// chat chunk with only the role, keepalive pings).
	Prelude Kind = iota
	// Content carries output.
	Content
	// Terminal ends the response.
	Terminal
	// Failed is an error reported inside the stream.
	Failed
)

// Gate sits between the upstream SSE reader and the host stream.
//
// CPA reads a plugin stream until the first chunk with a payload before it
// commits to the credential; an error before that is retried on another key.
// The gate holds the prelude events back until the first output event, so an
// error the upstream sends inside the stream before any output (rate limit,
// overload) still fails the call the same way an HTTP error would.
//
// It also records whether the response reached its terminal event, so a
// stream that just stops is reported as truncated instead of a normal end.
type Gate struct {
	config    Config
	sse       bool
	header    http.Header
	emit      func([]byte) error
	held      [][]byte
	heldBytes int
	open      bool
	done      bool
	failure   *pluginabi.Error
	// late is the error event that arrived after output had started.
	late *pluginabi.Error
	// Tap, when set, sees every event with its classification. The event
	// is nil for raw chunks of a body that is not SSE.
	Tap func(Kind, *Event)
}

// Config describes the stream a gate reads.
type Config struct {
	// Protocol is the protocol of the stream.
	Protocol provider.Protocol
	// Errors maps an error event before any output, given the HTTP status
	// it stands for, to the plugin error the call fails with, the same way
	// as an HTTP error; its prefix also starts the gate's own errors.
	Errors upstream.Errors
	// Fix, when set, repairs the stream's SSE events before the gate reads
	// them (provider.StreamFix).
	Fix provider.StreamFix
}

// New returns a gate for one stream; emit forwards events to the client.
func New(config Config, sse bool, header http.Header, emit func([]byte) error) *Gate {
	return &Gate{config: config, sse: sse, header: header, emit: emit}
}

// Push takes the next upstream event (or raw chunk for non-SSE bodies).
func (g *Gate) Push(event []byte) error {
	if !g.sse {
		g.open = true
		if g.Tap != nil {
			g.Tap(Content, nil)
		}
		return g.emit(event)
	}
	if g.config.Fix == nil {
		return g.push(event)
	}
	for _, fixed := range g.config.Fix.Event(event) {
		if errPush := g.push(fixed); errPush != nil {
			return errPush
		}
	}
	return nil
}

// push reads one SSE event as it is.
func (g *Gate) push(event []byte) error {
	ev := NewEvent(event)
	kind, detail := classifyStreamEvent(g.config.Protocol, ev)
	if g.Tap != nil {
		g.Tap(kind, ev)
	}
	switch kind {
	case Failed:
		if !g.open {
			g.held = nil
			g.failure = g.config.Errors.Upstream(streamErrorStatus(detail), g.header, detail)
			return errStreamRejected
		}
		// After output has started the client gets the error event as is;
		// StreamError still reports it.
		if g.late == nil {
			g.late = g.config.Errors.Upstream(streamErrorStatus(detail), g.header, detail)
		}
		g.done = true
	case Terminal:
		g.done = true
	case Prelude:
		if !g.open && g.heldBytes+len(event) <= maxPreludeBytes {
			g.held = append(g.held, event)
			g.heldBytes += len(event)
			return nil
		}
	}
	if errFlush := g.flush(); errFlush != nil {
		return errFlush
	}
	// "[DONE]" is kept for chat completions (CPA handles it there) and dropped
	// for the Responses and Messages protocols, which do not define it.
	if g.config.Protocol != provider.Chat && isDoneEvent(event) {
		return nil
	}
	return g.emit(event)
}

func (g *Gate) flush() error {
	g.open = true
	held := g.held
	g.held, g.heldBytes = nil, 0
	for _, event := range held {
		if errEmit := g.emit(event); errEmit != nil {
			return errEmit
		}
	}
	return nil
}

// Finish reports how the stream ended: nil when the response is complete,
// otherwise the error to close the host stream with.
func (g *Gate) Finish(ctx context.Context, errPump error) *pluginabi.Error {
	fix := g.config.Fix
	if fix != nil && g.sse && errPump == nil && g.failure == nil {
		for _, held := range fix.Flush() {
			if errPush := g.push(held); errPush != nil {
				errPump = errPush
				break
			}
		}
	}
	switch {
	case g.failure != nil:
		return g.failure
	case g.done:
		return nil
	case errPump != nil:
		return streamFailure(ctx, errPump, g.config.Errors.Prefix)
	case !g.sse:
		return nil
	case fix != nil && g.open:
		closing := fix.Close()
		if len(closing) == 0 {
			return streamTruncated(g.config.Errors.Prefix)
		}
		for _, event := range closing {
			if errPush := g.Push(event); errPush != nil {
				return streamFailure(ctx, errPush, g.config.Errors.Prefix)
			}
		}
		return nil
	default:
		return streamTruncated(g.config.Errors.Prefix)
	}
}

// streamTruncated reports an accepted stream that ended without its terminal
// event. CPA treats the stream_truncated code as a 502 that does not cool the
// credential down.
func streamTruncated(prefix string) *pluginabi.Error {
	return &pluginabi.Error{
		Code:       "stream_truncated",
		Message:    prefix + "upstream stream ended before the response finished",
		HTTPStatus: http.StatusBadGateway,
		Retryable:  true,
	}
}

// classifyStreamEvent sorts an SSE event by protocol. For Failed it
// also returns the error as a JSON body the error mapping can read.
//
// Messages and Responses events are told apart by their type, so only the
// few that need more (message_delta, response.failed) are decoded; chat
// chunks always are.
func classifyStreamEvent(protocol provider.Protocol, ev *Event) (Kind, []byte) {
	data := ev.Data()
	if len(data) == 0 {
		return Prelude, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return Terminal, nil
	}
	if !ev.Object() {
		// Unknown data: do not hold it back.
		return Content, nil
	}
	eventType := ev.stringMember("type")
	if eventType == "" {
		eventType = ev.Name()
	}
	if eventType == "error" || (ev.present("error") && eventType != "response.failed") {
		return Failed, data
	}
	switch protocol {
	case provider.Chat:
		payload := ev.Payload()
		if payload == nil {
			return Content, nil
		}
		return classifyChatChunk(payload), nil
	case provider.Responses:
		switch eventType {
		case "response.completed", "response.incomplete":
			return Terminal, nil
		case "response.failed":
			payload := ev.Payload()
			if payload == nil {
				return Content, nil
			}
			return Failed, responseFailure(payload)
		case "response.created", "response.queued", "response.in_progress", "response.output_item.added", "response.content_part.added":
			return Prelude, nil
		}
	case provider.Messages:
		switch eventType {
		case "message_stop":
			return Terminal, nil
		case "message_delta":
			payload := ev.Payload()
			if delta, _ := payload["delta"].(map[string]any); delta != nil && nonEmpty(delta["stop_reason"]) {
				return Terminal, nil
			}
		case "message_start", "ping", "content_block_start":
			return Prelude, nil
		}
	}
	return Content, nil
}

// classifyChatChunk treats a chunk with a finish reason as terminal and one
// with any delta besides the role as output.
func classifyChatChunk(payload map[string]any) Kind {
	choices, _ := payload["choices"].([]any)
	kind := Prelude
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if nonEmpty(choice["finish_reason"]) {
			return Terminal
		}
		delta, _ := choice["delta"].(map[string]any)
		for key, value := range delta {
			if key != "role" && nonEmpty(value) {
				kind = Content
			}
		}
	}
	return kind
}

// responseFailure turns a response.failed event into {"error":{...}}.
func responseFailure(payload map[string]any) []byte {
	response, _ := payload["response"].(map[string]any)
	failure, _ := response["error"].(map[string]any)
	if failure == nil {
		failure = map[string]any{"code": "response_failed", "message": "response failed"}
	}
	raw, _ := json.Marshal(map[string]any{"error": failure})
	return raw
}

// streamErrorStatus picks the HTTP status an in-stream error stands for, so
// the error mapping maps it the same way as the matching HTTP error.
func streamErrorStatus(body []byte) int {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	scopes := []map[string]any{payload}
	if inner, ok := payload["error"].(map[string]any); ok {
		scopes = append(scopes, inner)
	}
	var names []string
	for _, scope := range scopes {
		for _, key := range []string{"status", "status_code", "http_status", "code"} {
			if number, ok := scope[key].(float64); ok && number >= 400 && number <= 599 {
				return int(number)
			}
		}
		for _, key := range []string{"code", "type"} {
			if value := strings.ToLower(jsonfield.Text(scope[key])); value != "" {
				names = append(names, value)
			}
		}
	}
	text := strings.Join(names, " ")
	for _, rule := range streamErrorRules {
		for _, word := range rule.words {
			if strings.Contains(text, word) {
				return rule.status
			}
		}
	}
	return http.StatusBadGateway
}

type streamErrorRule struct {
	status int
	words  []string
}

// streamErrorRules map the code and type words of an in-stream error to the
// HTTP status it stands for, first match wins. One table serves every
// provider: besides the common words it has the ones OpenCode and OpenDesign
// report (budget and usage limits, insufficient funds, AuthError,
// MissingSession).
var streamErrorRules = []streamErrorRule{
	{http.StatusPaymentRequired, []string{"insufficient_balance", "insufficient_quota", "insufficient_funds", "insufficient account funds", "billing"}},
	{http.StatusTooManyRequests, []string{"rate_limit", "rate-limited", "too_many_requests", "budget exceeded", "budget_exceeded", "usage_limit", "usage limit"}},
	{529, []string{"overloaded"}},
	{http.StatusUnauthorized, []string{"authentication", "autherror", "invalid_api_key", "unauthorized"}},
	{http.StatusForbidden, []string{"request_blocked", "model_denylisted", "account_restricted", "permission"}},
	{http.StatusNotFound, []string{"not_found"}},
	{http.StatusGatewayTimeout, []string{"timeout"}},
	{http.StatusBadRequest, []string{"invalid_request", "context_length", "too_large", "missingsession"}},
}

func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// StreamError is the error event the upstream sent after output had
// started, mapped like one before it, or nil. The client gets that event
// inside the stream and Finish reports the stream as complete; the request
// log records it as a failure.
func (g *Gate) StreamError() *pluginabi.Error {
	return g.late
}

// Phase tells the request log whether output had reached the client.
func (g *Gate) Phase() string {
	if g.open {
		return "stream"
	}
	return "request"
}
