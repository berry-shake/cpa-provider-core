package payload

import (
	"encoding/json"
	"testing"
)

func TestSetString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"model":"wokey/a","messages":[{"model":"keep"}]}`, `{"model":"a","messages":[{"model":"keep"}]}`},
		{`{ "messages": [], "model" : "x", "n": 1.50 }`, `{ "messages": [], "model" : "a", "n": 1.50 }`},
		{`{"messages":[]}`, `{"model":"a","messages":[]}`},
		{`{}`, `{"model":"a"}`},
		{` { } `, ` {"model":"a" } `},
	}
	for _, tc := range cases {
		got, errSet := SetString([]byte(tc.in), "model", "a")
		if errSet != nil || string(got) != tc.want {
			t.Errorf("SetString(%s) = %s, %v; want %s", tc.in, got, errSet, tc.want)
		}
		if !json.Valid(got) {
			t.Errorf("invalid JSON: %s", got)
		}
	}
	for _, bad := range []string{``, `[]`, `"x"`, `{"model":}`} {
		if _, errSet := SetString([]byte(bad), "model", "a"); errSet == nil {
			t.Errorf("SetString(%q) accepted", bad)
		}
	}
}
