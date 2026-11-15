package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
)

// decodeExecutorRequest decodes the request of executor.execute,
// executor.execute_stream and executor.count_tokens. It answers like
// json.Unmarshal into rpcExecutorRequest, with one difference:
// OriginalRequest is decoded only when CPA did not report the requested
// model in Metadata, the one case the core reads it (clientModel); otherwise
// it is left nil and its value is not checked.
//
// CPA sends Payload and OriginalRequest as base64 strings, each the size of
// the client's request. encoding/json decodes them at about 200 MB/s, so a
// large request cost about 10 ms. Here jsonfield finds the two members, the
// rest of the document goes through encoding/json with their values replaced
// by null, and the base64 is decoded directly. Anything unusual (a value that
// is not a plain base64 string, a document jsonfield rejects) falls back to
// json.Unmarshal, so errors read exactly as before.
func decodeExecutorRequest(raw []byte) (rpcExecutorRequest, error) {
	if req, ok := decodeExecutorRequestFast(raw); ok {
		return req, nil
	}
	var req rpcExecutorRequest
	err := json.Unmarshal(raw, &req)
	return req, err
}

func decodeExecutorRequestFast(raw []byte) (rpcExecutorRequest, bool) {
	var req rpcExecutorRequest
	// encoding/json matches keys case-insensitively (bytes.EqualFold) and
	// decodes every duplicate member in turn: each one must be valid, the
	// last one wins. The spans are kept in document order for the same rules.
	var bulk, payloads, originals []span
	errScan := jsonfield.EachMember(raw, func(key []byte, start, end int) bool {
		switch {
		case bytes.EqualFold(key, []byte("Payload")):
			payloads = append(payloads, span{start, end})
		case bytes.EqualFold(key, []byte("OriginalRequest")):
			originals = append(originals, span{start, end})
		default:
			return true
		}
		bulk = append(bulk, span{start, end})
		return true
	})
	if errScan != nil || len(bulk) == 0 {
		return req, false
	}

	rest := make([]byte, 0, restLen(raw, bulk))
	at := 0
	for _, s := range bulk {
		rest = append(rest, raw[at:s.start]...)
		rest = append(rest, "null"...)
		at = s.end
	}
	rest = append(rest, raw[at:]...)
	if json.Unmarshal(rest, &req) != nil {
		return req, false
	}

	var ok bool
	if req.Payload, ok = decodeBase64Values(raw, payloads); !ok {
		return req, false
	}
	if requestedModel(req.Metadata) == "" {
		if req.OriginalRequest, ok = decodeBase64Values(raw, originals); !ok {
			return req, false
		}
	}
	return req, true
}

// decodeBase64Values decodes the occurrences of one []byte member in order,
// as encoding/json does, and returns the last; nil when there are none.
func decodeBase64Values(raw []byte, spans []span) (out []byte, ok bool) {
	for _, s := range spans {
		if out, ok = decodeBase64Value(raw[s.start:s.end]); !ok {
			return nil, false
		}
	}
	return out, true
}

// span is a member value's byte range inside the RPC request.
type span struct{ start, end int }

func restLen(raw []byte, bulk []span) int {
	n := len(raw)
	for _, s := range bulk {
		n -= s.end - s.start - len("null")
	}
	return n
}

// decodeBase64Value decodes one []byte member value the way encoding/json
// does: null is nil, a string is standard base64. ok is false for anything
// else, and for strings with escapes or line breaks, which
// base64.StdEncoding would read differently from encoding/json; the caller
// then falls back to json.Unmarshal.
func decodeBase64Value(value []byte) ([]byte, bool) {
	if string(value) == "null" {
		return nil, true
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return nil, false
	}
	encoded := value[1 : len(value)-1]
	if bytes.IndexByte(encoded, '\\') >= 0 || bytes.IndexByte(encoded, '\n') >= 0 || bytes.IndexByte(encoded, '\r') >= 0 {
		return nil, false
	}
	out := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, errDecode := base64.StdEncoding.Decode(out, encoded)
	if errDecode != nil {
		return nil, false
	}
	return out[:n], true
}

// requestedModel is the client's model name CPA reports in the executor
// metadata, or "".
func requestedModel(metadata map[string]any) string {
	requested, _ := metadata["requested_model"].(string)
	return strings.TrimSpace(requested)
}
