package core

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// accountCacheTTL is how long a complete account answer is reused. The
// providers' own clients poll their account endpoints about once a minute
// (Ollama allows 10 balance and usage requests a minute per user).
const accountCacheTTL = time.Minute

type rpcQuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// Account queries (balance, usage and the check made when a key is saved)
// go out one at a time per egress: a query waits for the one ahead of it on
// the same proxy, or the same direct line, to finish. Keys on different
// egresses are queried in parallel.

// accountTurn resolves the egress for a credential's proxy and waits for
// its turn, giving up when ctx ends. release must be called when done.
func (s *Service) accountTurn(ctx context.Context, proxyURL string) (client *http.Client, release func(), perr *pluginabi.Error) {
	setting, errProxy := egress.ResolveEgress(proxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, nil, upstream.RequestError("invalid_proxy", errProxy.Error())
	}
	release, errWait := s.accountQueue.Acquire(ctx, setting)
	if errWait != nil {
		return nil, nil, s.errors.Transport(fmt.Errorf("waiting for the egress: %w", errWait))
	}
	return s.clients.Client(setting), release, nil
}

// verifyKey checks a new key with the adapter, in its egress's turn; the
// whole check, waiting included, is bounded by AccountCallTimeout.
func (s *Service) verifyKey(cred credential.Credential) *pluginabi.Error {
	ctx, cancel := context.WithTimeout(s.ctx, provider.AccountCallTimeout)
	defer cancel()
	client, release, perr := s.accountTurn(ctx, cred.ProxyURL)
	if perr != nil {
		return perr
	}
	defer release()
	return s.adapter.VerifyKey(ctx, s.env(), client, cred)
}

// fetchAccount loads what the adapter reports about the account behind a
// key, in a turn of the key's egress; time spent waiting for the turn does
// not count toward the calls' timeouts. With view set it is the management
// page's query (provider.AccountViewer when implemented). A complete answer
// is reused for accountCacheTTL unless the adapter's AccountKey is empty.
func (s *Service) fetchAccount(cred credential.Credential, view bool) (any, *pluginabi.Error) {
	env := s.env()
	fetch := s.adapter.FetchAccount
	cacheKey := s.adapter.AccountKey(env, cred)
	if view && s.viewer != nil {
		fetch = s.viewer.FetchAccountView
		if cacheKey != "" {
			cacheKey += " view"
		}
	}
	if info, ok := s.accounts.get(cacheKey, s.now()); ok {
		return info, nil
	}
	client, release, perr := s.accountTurn(s.ctx, cred.ProxyURL)
	if perr != nil {
		return nil, perr
	}
	defer release()
	// The query ahead in the queue may have been for the same key.
	if info, ok := s.accounts.get(cacheKey, s.now()); ok {
		return info, nil
	}
	info, complete, perr := fetch(env, client, cred)
	if perr != nil {
		return nil, perr
	}
	if complete && cacheKey != "" {
		s.accounts.put(cacheKey, info, s.now())
	}
	return info, nil
}

// checkCredential runs an adapter's provider.CredentialCheck in a turn of
// the key's egress and turns a failure into the management answer.
func (s *Service) checkCredential(cred credential.Credential, check *provider.CredentialCheck) (pluginapi.ManagementResponse, bool) {
	ctx, cancel := context.WithTimeout(s.ctx, provider.AccountCallTimeout)
	defer cancel()
	perr := func() *pluginabi.Error {
		client, release, perr := s.accountTurn(ctx, cred.ProxyURL)
		if perr != nil {
			return perr
		}
		defer release()
		return check.Run(ctx, s.env(), client)
	}()
	if perr != nil {
		return jsonResponse(verifyStatus(perr), errorBody{Error: check.Code, Message: perr.Message}), true
	}
	return pluginapi.ManagementResponse{}, false
}

// quotaFetch answers quota.fetch; an adapter that is a provider.QuotaFetcher
// answers on its own. Call does not call rememberHost first (unless
// Spec.QuotaRPC.RememberHost): CPA sends quota requests without the host
// summary, and an empty one would wipe the cached global proxy.
func (s *Service) quotaFetch(req pluginapi.QuotaFetchRequest) []byte {
	if s.quotaFetcher != nil {
		resp, perr := s.quotaFetcher.FetchQuota(req)
		if perr != nil {
			return errorEnvelopeFrom(perr)
		}
		return okEnvelope(resp)
	}
	cred, _, errCred := credential.FromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil || cred.APIKey == "" {
		// Fall back to the auth file on disk when CPA sends no storage.
		if fileCred, ok := s.credentialByAuthIndex(req.AuthIndex); ok {
			cred = fileCred
		}
	}
	if cred.APIKey == "" {
		return errorEnvelopeFrom(upstream.AuthError(s.errors.Prefix + "credential has no api_key"))
	}
	info, perr := s.fetchAccount(cred, false)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	if s.quotaChecker != nil {
		if perr := s.quotaChecker.QuotaError(info); perr != nil {
			return errorEnvelopeFrom(perr)
		}
	}
	return okEnvelope(s.adapter.Quota(info))
}

// accountCache keeps complete account answers per key and origin.
type accountCache struct {
	mu      sync.Mutex
	entries map[string]accountCacheEntry
}

type accountCacheEntry struct {
	info any
	at   time.Time
}

func (c *accountCache) get(key string, now time.Time) (any, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || now.Before(entry.at) || now.Sub(entry.at) >= accountCacheTTL {
		return nil, false
	}
	return entry.info, true
}

func (c *accountCache) put(key string, info any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]accountCacheEntry)
	}
	for other, entry := range c.entries {
		if now.Sub(entry.at) >= accountCacheTTL {
			delete(c.entries, other)
		}
	}
	c.entries[key] = accountCacheEntry{info: info, at: now}
}
