package core

import (
	"encoding/json"
	"net/http"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestStatusReportsTheRuntime(t *testing.T) {
	svc, _ := newTestService(t, nil)
	rt := svc.status().Runtime
	if rt.GoVersion != runtime.Version() || rt.GOMAXPROCS != runtime.GOMAXPROCS(0) {
		t.Fatalf("runtime = %+v", rt)
	}
	if rt.HeapAlloc == 0 || rt.HeapInuse < rt.HeapAlloc || rt.Sys < rt.HeapInuse || rt.HeapReleased > rt.HeapIdle || rt.StackInuse == 0 || rt.Goroutines < 1 {
		t.Fatalf("implausible figures: %+v", rt)
	}

	// The management route carries the same view.
	resp := svc.handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: svc.paths.status})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, resp.Body)
	}
	var body struct {
		Runtime map[string]any `json:"runtime"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"go_version", "heap_alloc", "heap_inuse", "heap_idle", "heap_released", "stack_inuse", "sys", "num_gc", "goroutines", "gomaxprocs"} {
		if _, ok := body.Runtime[key]; !ok {
			t.Errorf("runtime has no %s: %v", key, body.Runtime)
		}
	}
}
