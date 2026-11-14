package core

import (
	"context"
	"time"

	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// keyID names a key in a keyed catalog without revealing it.
func keyID(cred credential.Credential) string {
	return credential.Fingerprint(cred.APIKey)
}

// catalogFor returns the catalog a credential's models are built from,
// loading it synchronously when it does not have them yet: the catalog while
// it is empty or, for a keyed catalog, the key's list while the catalog does
// not hold it (a key without api_key loads nothing).
func (s *Service) catalogFor(cred credential.Credential) provider.Catalog {
	snapshot := s.catalog.Current()
	if s.keyed != nil {
		if keyed, ok := snapshot.(provider.KeyedCatalog); (ok && keyed.KnowsKey(keyID(cred))) || cred.APIKey == "" {
			return snapshot
		}
		return s.loadKeyFor(cred)
	}
	if !empty(snapshot) {
		return snapshot
	}
	return s.loadCatalogFor(cred)
}

// modelInfos lists the CPA models of a credential.
func modelInfos(snapshot provider.Catalog, cred credential.Credential) []pluginapi.ModelInfo {
	if keyed, ok := snapshot.(provider.KeyedCatalog); ok {
		return keyed.KeyModelInfos(cred.AllModels, cred.Models, keyID(cred))
	}
	return snapshot.ModelInfos(cred.AllModels, cred.Models)
}

// revisionFor is the revision a credential's models are registered from:
// the catalog's, or for a keyed catalog the key's.
func revisionFor(snapshot provider.Catalog, cred credential.Credential) string {
	if keyed, ok := snapshot.(provider.KeyedCatalog); ok {
		return keyed.RevisionFor(keyID(cred))
	}
	return snapshot.Summary().Revision
}

// loadKeyFor loads a key's list of a keyed catalog synchronously through the
// credential's own egress and base URL.
func (s *Service) loadKeyFor(cred credential.Credential) provider.Catalog {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	setting, errProxy := egress.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return s.catalog.Current()
	}
	snapshot, errFetch := s.keyed.FetchKey(ctx, s.clients.Client(setting), cred.EffectiveBaseURL(s.config()), cred.APIKey, keyID(cred), s.userAgent(), s.now())
	if errFetch != nil {
		s.logf("warn", s.errors.Prefix+"model list unavailable", map[string]any{"error": errFetch.Error()})
	}
	return snapshot
}

// loadCatalogFor fetches the catalog synchronously through the credential's
// own egress, so a key that needs a proxy can still discover models.
func (s *Service) loadCatalogFor(cred credential.Credential) provider.Catalog {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	setting, errProxy := egress.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return s.catalog.Current()
	}
	snapshot, errFetch := s.catalog.Fetch(ctx, s.clients.Client(setting), cred.EffectiveBaseURL(s.config()), s.userAgent(), false, s.now())
	if errFetch != nil {
		s.logf("warn", s.errors.Prefix+"model catalog unavailable", map[string]any{"error": errFetch.Error()})
	}
	return snapshot
}

func (s *Service) kickRefresh() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// refreshLoop polls the catalog and pushes changes to CPA.
func (s *Service) refreshLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
		case <-timer.C:
		}
		s.refreshCatalog(false)
		if interval := s.config().ModelsRefreshInterval; interval > 0 {
			timer.Reset(interval)
		}
	}
}

// refreshCatalog fetches the catalog (trying the global egress first, then
// each distinct credential proxy) and re-registers credentials whose models
// were built from an older catalog. A keyed catalog is refreshed by
// refreshKeys instead.
func (s *Service) refreshCatalog(force bool) (provider.Catalog, error) {
	if s.keyed != nil {
		return s.refreshKeys()
	}
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	entries, _ := s.listAuths()
	proxies := []string{""}
	seen := map[string]bool{"": true}
	for _, entry := range entries {
		if entry.readError == "" && !seen[entry.cred.ProxyURL] {
			seen[entry.cred.ProxyURL] = true
			proxies = append(proxies, entry.cred.ProxyURL)
		}
	}
	// When no egress resolves, the result is an empty catalog and no error.
	var (
		snapshot provider.Catalog = emptyCatalog{}
		errFetch error
	)
	for _, proxy := range proxies {
		setting, errProxy := egress.ResolveEgress(proxy, s.globalProxy())
		if errProxy != nil {
			continue
		}
		snapshot, errFetch = s.catalog.Fetch(ctx, s.clients.Client(setting), s.config().BaseURL, s.userAgent(), force, s.now())
		if errFetch == nil {
			break
		}
	}
	if errFetch != nil {
		s.logf("warn", s.errors.Prefix+"model catalog refresh failed", map[string]any{"error": errFetch.Error()})
		return snapshot, errFetch
	}
	s.syncRegistrations(snapshot, entries)
	return snapshot, nil
}

// refreshKeys reloads the list of every key of a keyed catalog (each
// through its own egress and base URL), drops the lists of deleted keys and
// re-registers credentials whose models were built from an older list. It
// fails only when no list loads.
func (s *Service) refreshKeys() (provider.Catalog, error) {
	entries, _ := s.listAuths()
	keep := map[string]bool{}
	var (
		loaded  int
		lastErr error
	)
	for _, entry := range entries {
		if entry.readError != "" || entry.cred.APIKey == "" || keep[keyID(entry.cred)] {
			continue
		}
		keep[keyID(entry.cred)] = true
		setting, errProxy := egress.ResolveEgress(entry.cred.ProxyURL, s.globalProxy())
		if errProxy != nil {
			lastErr = errProxy
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
		_, errFetch := s.keyed.FetchKey(ctx, s.clients.Client(setting), entry.cred.EffectiveBaseURL(s.config()), entry.cred.APIKey, keyID(entry.cred), s.userAgent(), s.now())
		cancel()
		if errFetch != nil {
			lastErr = errFetch
			continue
		}
		loaded++
	}
	snapshot := s.catalog.Current()
	if keyed, ok := snapshot.(provider.KeyedCatalog); len(entries) > 0 || (ok && keyed.Keys() > 0) {
		snapshot = s.keyed.Retain(keep)
	}
	if loaded == 0 && lastErr != nil {
		s.logf("warn", s.errors.Prefix+"model list refresh failed", map[string]any{"error": lastErr.Error()})
		return snapshot, lastErr
	}
	if lastErr != nil {
		s.logf("warn", s.errors.Prefix+"some model lists did not load", map[string]any{"error": lastErr.Error()})
	}
	s.syncRegistrations(snapshot, entries)
	return snapshot, nil
}

// syncRegistrations re-saves auth files whose registered models predate the
// current catalog (for a keyed catalog: their key's list). Saving makes CPA
// re-parse the file and call model.for_auth again. model_revision records
// the revision a file was last synced with; the in-memory registered map
// catches enabled credentials whose models were built from an older catalog.
// Disabled credentials are not registered by CPA, so only their file
// revision is checked and they are touched once per change.
func (s *Service) syncRegistrations(snapshot provider.Catalog, entries []authEntry) {
	for _, entry := range entries {
		if entry.readError != "" || !credential.IsProvider(entry.fields, s.spec.ID) {
			continue
		}
		revision := revisionFor(snapshot, entry.cred)
		if revision == "" {
			continue
		}
		stale := entry.cred.ModelRevision != revision
		if !entry.Disabled {
			if registered, known := s.registered.Load(entry.ID); known && registered != revision {
				stale = true
			}
		}
		if !stale {
			continue
		}
		name := credential.SaveName(entry.Name, entry.Path)
		if name == "" {
			continue
		}
		fields := cloneFields(entry.fields)
		fields[credential.KeyModelRevision] = revision
		if errSave := s.saveAuthFile(name, fields); errSave != nil {
			s.logf("warn", s.errors.Prefix+"model sync save failed", map[string]any{"auth": name, "error": errSave.Error()})
			continue
		}
		s.logf("info", s.errors.Prefix+"refreshed registered models", map[string]any{"auth": name, "revision": revision})
	}
}

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		out[key] = value
	}
	return out
}
