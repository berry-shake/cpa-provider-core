// Package payload edits request bodies in place at the top level only, so
// members the plugin does not touch reach the upstream byte for byte.
// Requests can run to megabytes, so they are scanned for the few members the
// plugin needs instead of being decoded.
package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
)

// Span is the byte range of a member value inside a body.
type Span struct{ Start, End int }

// Members finds the wanted top-level members of a JSON object and keeps the
// first occurrence of each. The scan stops once all of them are found, so a
// member near the start is cheap even on a large body. errScan reports a
// body that is not a well-formed object, met before the scan stopped; the
// members found up to that point are still returned.
func Members(body []byte, keys ...string) (found map[string]Span, errScan error) {
	found = make(map[string]Span, len(keys))
	errScan = jsonfield.EachMember(body, func(key []byte, start, end int) bool {
		for _, want := range keys {
			if string(key) == want {
				if _, seen := found[want]; !seen {
					found[want] = Span{start, end}
				}
				break
			}
		}
		return len(found) < len(keys)
	})
	if errScan != nil {
		errScan = scanError(errScan)
	}
	return found, errScan
}

func scanError(errScan error) error {
	if errors.Is(errScan, jsonfield.ErrNotObject) {
		return fmt.Errorf("payload is not a JSON object")
	}
	return fmt.Errorf("payload is not JSON: %w", errScan)
}

// TopLevelString returns a string member of a JSON object, or "".
func TopLevelString(body []byte, key string) string {
	found, _ := Members(body, key)
	return String(body, found, key)
}

// String decodes a string member found by Members, or "".
func String(body []byte, found map[string]Span, key string) string {
	at, ok := found[key]
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(body[at.Start:at.End], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// Value decodes a member found by Members; ok is false when it is absent or
// not valid JSON.
func Value(body []byte, found map[string]Span, key string) (value any, ok bool) {
	at, present := found[key]
	if !present || json.Unmarshal(body[at.Start:at.End], &value) != nil {
		return nil, false
	}
	return value, true
}

// Raw returns the bytes of a member found by Members, or nil.
func Raw(body []byte, found map[string]Span, key string) []byte {
	at, ok := found[key]
	if !ok {
		return nil
	}
	return body[at.Start:at.End]
}

// Set replaces the value of a member found by Members. A member the scan did
// not find is inserted, unless the scan stopped on a malformed body, which is
// returned as the error.
func Set(body []byte, found map[string]Span, errScan error, key string, rawValue []byte) ([]byte, error) {
	if at, ok := found[key]; ok {
		out := make([]byte, 0, len(body)-(at.End-at.Start)+len(rawValue))
		out = append(out, body[:at.Start]...)
		out = append(out, rawValue...)
		out = append(out, body[at.End:]...)
		return out, nil
	}
	if errScan != nil {
		return nil, errScan
	}
	return Insert(body, key, rawValue), nil
}

// SetString sets a top-level string member, inserting it when absent.
func SetString(body []byte, key, value string) ([]byte, error) {
	encoded, _ := json.Marshal(value)
	return SetRaw(body, key, encoded)
}

// SetRaw sets a top-level member to a JSON value, inserting it when absent.
// Unlike Set it scans the body itself, for a body edited since its scan.
func SetRaw(body []byte, key string, rawValue []byte) ([]byte, error) {
	found, errScan := Members(body, key)
	return Set(body, found, errScan, key, rawValue)
}

// Insert adds a member at the start of a JSON object.
func Insert(body []byte, key string, rawValue []byte) []byte {
	open := bytes.IndexByte(body, '{')
	if open < 0 {
		return body
	}
	encodedKey, _ := json.Marshal(key)
	member := append(append(append([]byte{}, encodedKey...), ':'), rawValue...)
	rest := bytes.TrimLeft(body[open+1:], " \t\r\n")
	out := make([]byte, 0, len(body)+len(member)+1)
	out = append(out, body[:open+1]...)
	out = append(out, member...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	out = append(out, body[open+1:]...)
	return out
}
