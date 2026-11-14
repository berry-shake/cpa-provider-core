package core

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// keyedCatalog is a provider.KeyedCatalogSource whose key lists are one
// model per key; fail makes FetchKey fail for an API key.
type keyedCatalog struct {
	mu    sync.Mutex
	lists map[string][]string
	fail  map[string]error
}

func (c *keyedCatalog) Current() provider.Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *keyedCatalog) snapshotLocked() keyedSnapshot {
	lists := make(map[string][]string, len(c.lists))
	for key, models := range c.lists {
		lists[key] = models
	}
	return keyedSnapshot{lists: lists}
}

func (c *keyedCatalog) Fetch(context.Context, *http.Client, string, string, bool, time.Time) (provider.Catalog, error) {
	return c.Current(), errors.New("lists load per key")
}

func (c *keyedCatalog) FetchKey(_ context.Context, _ *http.Client, _, apiKey, keyID, _ string, _ time.Time) (provider.Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.fail[apiKey]; err != nil {
		return c.snapshotLocked(), err
	}
	c.lists[keyID] = []string{"model-" + apiKey}
	return c.snapshotLocked(), nil
}

func (c *keyedCatalog) Retain(keep map[string]bool) provider.Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.lists {
		if !keep[key] {
			delete(c.lists, key)
		}
	}
	return c.snapshotLocked()
}

type keyedSnapshot struct{ lists map[string][]string }

func (s keyedSnapshot) models() []string {
	var out []string
	for _, models := range s.lists {
		out = append(out, models...)
	}
	sort.Strings(out)
	return out
}

func (s keyedSnapshot) Summary() provider.CatalogSummary {
	models := s.models()
	return provider.CatalogSummary{Count: len(models), Revision: strings.Join(models, ",")}
}
func (s keyedSnapshot) Has(id string) bool                { return slices.Contains(s.models(), id) }
func (s keyedSnapshot) UpstreamID(model, _ string) string { return model }
func (s keyedSnapshot) ModelsView() any                   { return s.models() }
func (s keyedSnapshot) ModelInfos(all bool, ids []string) []pluginapi.ModelInfo {
	return s.KeyModelInfos(all, ids, "")
}
func (s keyedSnapshot) KnowsKey(keyID string) bool { _, ok := s.lists[keyID]; return ok }
func (s keyedSnapshot) Keys() int                  { return len(s.lists) }
func (s keyedSnapshot) RevisionFor(keyID string) string {
	if models, ok := s.lists[keyID]; ok {
		return strings.Join(models, ",")
	}
	return s.Summary().Revision
}
func (s keyedSnapshot) KeyModelInfos(_ bool, _ []string, keyID string) []pluginapi.ModelInfo {
	var out []pluginapi.ModelInfo
	for _, id := range s.lists[keyID] {
		out = append(out, pluginapi.ModelInfo{ID: id})
	}
	return out
}
func (s keyedSnapshot) KeyHas(keyID, id string) bool { return slices.Contains(s.lists[keyID], id) }

// keyedAdapter is the test adapter with a keyed catalog.
type keyedAdapter struct {
	*testAdapter
	keyed *keyedCatalog
}

func (a *keyedAdapter) Catalog() provider.CatalogSource { return a.keyed }

// A refresh reports every distinct failure of the key lists, and keeps every
// list when the auth files cannot be listed.
func TestRefreshKeysFailures(t *testing.T) {
	host := newFakeHost(t)
	cat := &keyedCatalog{lists: map[string][]string{}, fail: map[string]error{}}
	svc := New(&keyedAdapter{testAdapter: newTestAdapter(), keyed: cat}, "test", host.call)
	host.svc = svc
	t.Cleanup(svc.Close)
	host.seed("k1.json", testProviderID, map[string]any{"type": testProviderID, "api_key": "one"})
	host.seed("k2.json", testProviderID, map[string]any{"type": testProviderID, "api_key": "two"})
	host.seed("k3.json", testProviderID, map[string]any{"type": testProviderID, "api_key": "three"})

	cat.fail["two"] = errors.New("two is down")
	cat.fail["three"] = errors.New("three is down")
	snapshot, errRefresh := svc.refreshCatalog(false)
	if errRefresh != nil || snapshot.(provider.KeyedCatalog).Keys() != 1 {
		t.Fatalf("partial refresh = %v, %d keys", errRefresh, snapshot.(provider.KeyedCatalog).Keys())
	}

	cat.fail["one"] = errors.New("three is down")
	if _, errRefresh = svc.refreshCatalog(false); errRefresh == nil || errRefresh.Error() != "three is down; two is down" {
		t.Fatalf("failed refresh = %v, want both distinct errors", errRefresh)
	}

	host.mu.Lock()
	host.listFails = true
	host.mu.Unlock()
	snapshot, errRefresh = svc.refreshCatalog(false)
	if errRefresh == nil || !strings.Contains(errRefresh.Error(), "auth list unavailable") {
		t.Fatalf("refresh without auth list = %v", errRefresh)
	}
	if keyed := snapshot.(provider.KeyedCatalog); keyed.Keys() != 1 || !keyed.KnowsKey(credential.Fingerprint("one")) {
		t.Fatalf("lists after a failed auth list = %d keys", keyed.Keys())
	}
}
