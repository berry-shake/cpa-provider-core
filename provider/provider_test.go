package provider

import "testing"

func TestProtocolForFormat(t *testing.T) {
	cases := map[string]Protocol{
		"openai":           Chat,
		"chat-completions": Chat,
		"openai-response":  Responses,
		"responses":        Responses,
		"claude":           Messages,
		"Anthropic":        Messages,
	}
	for format, want := range cases {
		if got, ok := ProtocolForFormat(format); !ok || got != want {
			t.Errorf("ProtocolForFormat(%q) = %q, %v", format, got, ok)
		}
	}
	if _, ok := ProtocolForFormat("gemini"); ok {
		t.Error("gemini accepted")
	}
}
