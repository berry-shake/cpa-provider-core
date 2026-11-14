package core

import (
	"net/http"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// extAdapter is the test adapter with the hooks OpenDesign uses that need
// no upstream: HostUser, ManagementExtension, QuotaFetcher and QuotaPage.
type extAdapter struct {
	*testAdapter
	host provider.Host
}

func (a *extAdapter) Attach(host provider.Host) { a.host = host }

func (a *extAdapter) ManagementRoutes() []provider.ManagementRoute {
	return []provider.ManagementRoute{{
		Method: http.MethodGet, Path: "/echo", Description: "Echo ?q=.",
		Handle: func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
			return provider.JSONResponse(http.StatusOK, map[string]any{"q": req.Query.Get("q")})
		},
	}}
}

// FetchQuota counts the provider's auth files the host lists.
func (a *extAdapter) FetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error) {
	files, errList := a.host.AuthFiles()
	if errList != nil {
		return pluginapi.QuotaFetchResponse{}, &pluginabi.Error{Code: "host_unavailable", Message: errList.Error(), HTTPStatus: http.StatusBadGateway}
	}
	return pluginapi.QuotaFetchResponse{Summary: []pluginapi.QuotaMetric{{Key: "files", Value: float64(len(files))}}}, nil
}

func (a *extAdapter) ServeQuota(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return provider.JSONResponse(http.StatusOK, map[string]any{"page": req.Query.Get("auth_index")})
}

func newExtService(t *testing.T) (*Service, *extAdapter, *fakeHost) {
	t.Helper()
	host := newFakeHost(t)
	adapter := &extAdapter{testAdapter: newTestAdapter()}
	svc := New(adapter, "test", host.call)
	host.svc = svc
	t.Cleanup(svc.Close)
	if adapter.host == nil {
		t.Fatal("New did not attach the host")
	}
	return svc, adapter, host
}

// Adapter routes are declared after the quota route and served under the
// plugin's management base; a wrong method is 405, and QuotaPage serves the
// quota route.
func TestManagementExtension(t *testing.T) {
	svc, _, _ := newExtService(t)
	reg := svc.managementRoutes()
	var paths []string
	for _, route := range reg.Routes {
		paths = append(paths, route.Method+" "+route.Path)
	}
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "GET "+managementBase+"/credentials/quota\nGET "+managementBase+"/echo\n") {
		t.Fatalf("routes:\n%s", joined)
	}
	if status, out := manage(t, svc, http.MethodGet, managementBase+"/echo?q=hi", ""); status != http.StatusOK || out["q"] != "hi" {
		t.Fatalf("GET echo = %d %v", status, out)
	}
	if status, out := manage(t, svc, http.MethodPost, managementBase+"/echo", ""); status != http.StatusMethodNotAllowed || out["error"] != "method_not_allowed" {
		t.Fatalf("POST echo = %d %v", status, out)
	}
	if status, out := manage(t, svc, http.MethodGet, managementBase+"/credentials/quota?auth_index=idx-7", ""); status != http.StatusOK || out["page"] != "idx-7" {
		t.Fatalf("quota page = %d %v", status, out)
	}
}

// A QuotaFetcher answers quota.fetch, which leaves the cached host summary
// alone; quota.reset is refused the same way for every provider, and the
// Host lists only the provider's own auth files.
func TestQuotaRPCAndHost(t *testing.T) {
	svc, adapter, host := newExtService(t)
	host.seed("mine.json", testProviderID, map[string]any{"type": testProviderID, "api_key": testGoodKey})
	host.seed("other.json", "codex", map[string]any{"type": "codex"})

	env := decodeEnvelope(t, svc.Call(pluginabi.MethodQuotaFetch, []byte("not json")))
	if env.OK || env.Error == nil || !strings.HasPrefix(env.Error.Message, "decode quota request: ") {
		t.Fatalf("decode error = %+v", env.Error)
	}

	svc.rememberHost(pluginapi.HostConfigSummary{ProxyURL: "http://proxy.invalid:3128"})
	env = decodeEnvelope(t, svc.Call(pluginabi.MethodQuotaFetch, mustJSON(t, map[string]any{
		"auth_index": "idx-1", "provider": testProviderID,
	})))
	if !env.OK {
		t.Fatalf("fetch = %+v", env.Error)
	}
	quota := mustDecode[pluginapi.QuotaFetchResponse](t, env.Result)
	if len(quota.Summary) != 1 || quota.Summary[0].Value != 1 {
		t.Fatalf("quota = %s", env.Result)
	}
	if proxy := svc.globalProxy(); proxy != "http://proxy.invalid:3128" {
		t.Fatalf("global proxy = %q, quota.fetch without a host summary must keep it", proxy)
	}

	env = decodeEnvelope(t, svc.Call(pluginabi.MethodQuotaReset, []byte(`{}`)))
	if reset := mustDecode[pluginapi.QuotaResetResponse](t, env.Result); !env.OK || reset.Success || reset.Message != "wokey: quota reset is not supported" {
		t.Fatalf("reset = %s %+v", env.Result, env.Error)
	}

	files, errList := adapter.host.AuthFiles()
	if errList != nil || len(files) != 1 || files[0].Credential.APIKey != testGoodKey || files[0].ReadError != "" {
		t.Fatalf("auth files = %+v, %v", files, errList)
	}
	fields, name, errRead := adapter.host.ReadAuthFile(files[0].AuthIndex)
	if errRead != nil || name != "mine.json" || fields["api_key"] != testGoodKey {
		t.Fatalf("read = %v %q %v", fields, name, errRead)
	}
}
