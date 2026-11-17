package core

import (
	"net/http"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-provider-core/credential"
)

func TestKeepHiddenPassword(t *testing.T) {
	const saved = "socks5h://user:secret@proxy.example:1080"
	cases := []struct {
		name, proxy, saved, want, err string
	}{
		{"port changed", "socks5h://user:xxxxx@proxy.example:1081", saved, "socks5h://user:secret@proxy.example:1081", ""},
		{"unchanged", "socks5h://user:xxxxx@proxy.example:1080", saved, saved, ""},
		{"host case", "socks5h://user:xxxxx@PROXY.example:1080", saved, "socks5h://user:secret@PROXY.example:1080", ""},
		{"escaped password", "http://u:xxxxx@h:2", "http://u:p%40ss@h:1", "http://u:p%40ss@h:2", ""},
		{"empty saved password", "http://u:xxxxx@h:2", "http://u:@h:1", "http://u:@h:2", ""},
		{"new password", "socks5h://user:other@proxy.example:1081", saved, "socks5h://user:other@proxy.example:1081", ""},
		{"no password", "socks5h://proxy.example:1081", saved, "socks5h://proxy.example:1081", ""},
		{"nothing saved", "http://u:xxxxx@h:1", "", "http://u:xxxxx@h:1", ""},
		{"saved without password", "http://u:xxxxx@h:1", "http://u@h:1", "http://u:xxxxx@h:1", ""},
		{"saved direct", "http://u:xxxxx@h:1", "direct", "http://u:xxxxx@h:1", ""},
		{"other host", "socks5h://user:xxxxx@elsewhere.example:1080", saved, "", "proxy_password_hidden"},
		{"other user", "socks5h://admin:xxxxx@proxy.example:1080", saved, "", "proxy_password_hidden"},
		{"other scheme", "http://user:xxxxx@proxy.example:1080", saved, "", "proxy_password_hidden"},
		{"cleared", "", saved, "", ""},
		{"direct", "direct", saved, "direct", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, errBody := keepHiddenPassword(tc.proxy, tc.saved)
			if tc.err != "" {
				if errBody == nil || errBody.Error != tc.err {
					t.Fatalf("error = %v, want %s", errBody, tc.err)
				}
				if strings.Contains(errBody.Message, "secret") {
					t.Fatalf("the message must not carry the saved password: %q", errBody.Message)
				}
				return
			}
			if errBody != nil {
				t.Fatalf("unexpected error %v", errBody)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The edit form sends back the redacted address with only the port changed.
// skip_verify keeps the test off the network: the proxies are made up.
func TestUpdateCredentialKeepsHiddenProxyPassword(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`","skip_verify":true,"proxy_url":"socks5h://user:secret@127.0.0.1:9"}`)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	name := credential.FileNameForKey(testProviderID, testGoodKey)
	index := host.file(name).index
	shown, _ := credentialOf(t, out)["proxy_url"].(string)
	if shown != "socks5h://user:xxxxx@127.0.0.1:9" {
		t.Fatalf("view proxy_url = %q", shown)
	}

	edited := strings.Replace(shown, ":9", ":10", 1)
	status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","skip_verify":true,"proxy_url":"`+edited+`"}`)
	if status != http.StatusOK {
		t.Fatalf("patch: %d %v", status, out)
	}
	if got := host.fields(name)[credential.KeyProxyURL]; got != "socks5h://user:secret@127.0.0.1:10" {
		t.Fatalf("saved proxy_url = %v, want the saved password on the new port", got)
	}

	saves := host.saveCount()
	status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","skip_verify":true,"proxy_url":"socks5h://user:xxxxx@127.0.0.2:10"}`)
	if status != http.StatusBadRequest || out["error"] != "proxy_password_hidden" {
		t.Fatalf("other host: %d %v", status, out)
	}
	if host.saveCount() != saves || host.fields(name)[credential.KeyProxyURL] != "socks5h://user:secret@127.0.0.1:10" {
		t.Fatal("a refused edit must not be saved")
	}

	// Adding has no saved password: xxxxx is the password typed.
	status, out = manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"sk-offline-key-123456","skip_verify":true,"proxy_url":"http://u:xxxxx@127.0.0.1:11"}`)
	if status != http.StatusCreated {
		t.Fatalf("add with xxxxx: %d %v", status, out)
	}
	if got := host.fields(credential.FileNameForKey(testProviderID, "sk-offline-key-123456"))[credential.KeyProxyURL]; got != "http://u:xxxxx@127.0.0.1:11" {
		t.Fatalf("added proxy_url = %v", got)
	}
}
