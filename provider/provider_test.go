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

func TestSpecProtocols(t *testing.T) {
	all := Spec{}
	if !all.Serves(Messages) || len(all.ServedProtocols()) != 3 {
		t.Fatalf("default spec serves %v", all.ServedProtocols())
	}
	chat := Spec{Protocols: []Protocol{Chat}}
	if !chat.Serves(Chat) || chat.Serves(Responses) || chat.Serves(Messages) {
		t.Fatal("chat-only spec serves more than chat")
	}
	// The order follows Protocols, not the declaration.
	two := Spec{Protocols: []Protocol{Messages, Chat}}
	if got := two.ServedProtocols(); len(got) != 2 || got[0] != Chat || got[1] != Messages {
		t.Fatalf("served = %v", got)
	}
}
