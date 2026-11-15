package core

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// wantExecutorRequest is what decodeExecutorRequest must answer for raw:
// json.Unmarshal's result, without OriginalRequest when CPA reported the
// requested model.
func wantExecutorRequest(raw []byte) (rpcExecutorRequest, error) {
	var want rpcExecutorRequest
	err := json.Unmarshal(raw, &want)
	if err == nil && requestedModel(want.Metadata) != "" {
		want.OriginalRequest = nil
	}
	return want, err
}

func checkDecode(t *testing.T, raw []byte) {
	t.Helper()
	want, errWant := wantExecutorRequest(raw)
	got, errGot := decodeExecutorRequest(raw)
	if (errGot == nil) != (errWant == nil) || errGot != nil && errGot.Error() != errWant.Error() {
		t.Fatalf("error = %v, want %v\nraw: %.300s", errGot, errWant, raw)
	}
	if errGot == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v\nwant    %+v\nraw: %.300s", got, want, raw)
	}
}

func TestDecodeExecutorRequestMatchesJSON(t *testing.T) {
	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i)
	}
	body := []byte(`{"model":"wokey/model-a","messages":[{"role":"user","content":"hi <b>&</b> \"x\""}]}`)
	cases := map[string]rpcExecutorRequest{
		"requested model": {ExecutorRequest: pluginapi.ExecutorRequest{
			AuthID: "wokey-1.json", Model: "model-a", Format: "claude", Stream: true,
			Headers: http.Header{"User-Agent": {"claude-cli/2.1"}}, Query: url.Values{"beta": {"true"}},
			Payload: body, OriginalRequest: body,
			Metadata:     map[string]any{"requested_model": "wokey/model-a"},
			StorageJSON:  []byte(`{"type":"wokey","api_key":"wk-1"}`),
			AuthMetadata: map[string]any{"k": 1.5}, AuthAttributes: map[string]string{"a": "b"},
		}, StreamID: "s1", HostCallbackID: "h1"},
		"no requested model": {ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "model-a", Payload: body, OriginalRequest: body,
		}},
		"blank requested model": {ExecutorRequest: pluginapi.ExecutorRequest{
			Payload: body, OriginalRequest: body, Metadata: map[string]any{"requested_model": "  "},
		}},
		"non-string requested model": {ExecutorRequest: pluginapi.ExecutorRequest{
			Payload: body, OriginalRequest: body, Metadata: map[string]any{"requested_model": 7},
		}},
		"binary payload": {ExecutorRequest: pluginapi.ExecutorRequest{Payload: binary, OriginalRequest: binary}},
		"empty payload":  {ExecutorRequest: pluginapi.ExecutorRequest{Payload: []byte{}, OriginalRequest: []byte{}}},
		"nil payload":    {ExecutorRequest: pluginapi.ExecutorRequest{Model: "m"}},
		"payload only":   {ExecutorRequest: pluginapi.ExecutorRequest{Payload: body}},
		"large payload":  {ExecutorRequest: pluginapi.ExecutorRequest{Payload: benchRequestBody(64 << 10), OriginalRequest: benchRequestBody(64 << 10)}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			raw, errMarshal := json.Marshal(req)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			checkDecode(t, raw)
		})
	}
}

func TestDecodeExecutorRequestEdgeCases(t *testing.T) {
	cases := map[string]string{
		"lower-case key":           `{"payload":"eyJhIjoxfQ==","Model":"m"}`,
		"duplicate payload":        `{"Payload":"eyJhIjoxfQ==","Payload":"eyJiIjoyfQ=="}`,
		"duplicate, then null":     `{"Payload":"eyJhIjoxfQ==","payload":null}`,
		"duplicate, first invalid": `{"pAYloAd":"0","PAYloAd":null}`, // found by FuzzDecodeExecutorRequest
		"null payload":             `{"Payload":null,"OriginalRequest":null}`,
		"escaped slash":            `{"Payload":"YS9i\/Yw=="}`,
		"escaped quote":            `{"Payload":"\"x"}`,
		"payload number":           `{"Payload":123,"Model":"m"}`,
		"payload object":           `{"Payload":{"a":1}}`,
		"invalid base64":           `{"Payload":"!!!!"}`,
		"base64 with newline":      "{\"Payload\":\"eyJh\nIjoxfQ==\"}",
		"type error elsewhere":     `{"Payload":"eyJhIjoxfQ==","Stream":"yes"}`,
		"trailing garbage":         `{"Payload":"eyJhIjoxfQ=="} x`,
		"not an object":            `["Payload"]`,
		"truncated":                `{"Payload":"eyJhIjox`,
		"bad literal elsewhere":    `{"Payload":"eyJhIjoxfQ==","Stream":tru}`,
		"no bulk members":          `{"Model":"m","Stream":true}`,
		"original, no metadata":    `{"Payload":"eyJhIjoxfQ==","OriginalRequest":"eyJtb2RlbCI6IngifQ=="}`,
		"original, reported":       `{"OriginalRequest":"eyJtb2RlbCI6IngifQ==","Metadata":{"requested_model":"x"}}`,
		"empty object":             `{}`,
		"whitespace":               " \n{ \"Payload\" : \"eyJhIjoxfQ==\" , \"Model\" : \"m\" } \n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { checkDecode(t, []byte(raw)) })
	}
}

// The one intended difference from json.Unmarshal: an OriginalRequest the
// core does not need is neither decoded nor checked.
func TestDecodeExecutorRequestSkipsUnneededOriginal(t *testing.T) {
	raw := []byte(`{"Payload":"eyJhIjoxfQ==","OriginalRequest":"not base64!","Metadata":{"requested_model":"wokey/m"}}`)
	var plain rpcExecutorRequest
	if json.Unmarshal(raw, &plain) == nil {
		t.Fatal("encoding/json accepted invalid base64")
	}
	got, errGot := decodeExecutorRequest(raw)
	if errGot != nil {
		t.Fatalf("error = %v", errGot)
	}
	if got.OriginalRequest != nil || string(got.Payload) != `{"a":1}` || requestedModel(got.Metadata) != "wokey/m" {
		t.Fatalf("decoded %+v", got)
	}

	// Without the reported model it is needed, checked and rejected as before.
	raw = []byte(`{"Payload":"eyJhIjoxfQ==","OriginalRequest":"not base64!"}`)
	checkDecode(t, raw)
	if _, errGot = decodeExecutorRequest(raw); errGot == nil {
		t.Fatal("invalid OriginalRequest accepted though it was needed")
	}
}

func FuzzDecodeExecutorRequest(f *testing.F) {
	for _, seed := range []string{
		`{"Payload":"eyJhIjoxfQ==","OriginalRequest":"eyJtb2RlbCI6IngifQ==","Model":"m","Stream":true}`,
		`{"payload":"eyJhIjoxfQ==","Payload":null,"Metadata":{"requested_model":"x"}}`,
		`{"Payload":"YS9i\/Yw==","Headers":{"A":["b"]},"stream_id":"s"}`,
		`{"Payload":123}`,
		`{"OriginalRequest":"!!","Metadata":{"requested_model":""}}`,
		`{}`,
		`[]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		got, errGot := decodeExecutorRequest(raw)
		var plain rpcExecutorRequest
		errPlain := json.Unmarshal(raw, &plain)
		if errGot != nil {
			// Failures always come from the json.Unmarshal fallback.
			if errPlain == nil || errGot.Error() != errPlain.Error() {
				t.Fatalf("error = %v, json.Unmarshal: %v", errGot, errPlain)
			}
			return
		}
		skipped := requestedModel(got.Metadata) != ""
		if errPlain != nil {
			// Only an OriginalRequest left out may hide an error.
			if !skipped || got.OriginalRequest != nil {
				t.Fatalf("accepted what json.Unmarshal rejects: %v", errPlain)
			}
			return
		}
		if skipped {
			plain.OriginalRequest = nil
		}
		if !reflect.DeepEqual(got, plain) {
			t.Fatalf("decoded %+v\njson    %+v", got, plain)
		}
	})
}
