package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The service tests run the core with testAdapter, a provider shaped like
// Wokey (the plugin they come from): its fake upstream (fakehost_test.go)
// serves Wokey's endpoints, and the adapter implements every optional
// interface the way Wokey does, so the tests also cover the hooks.

const (
	endpointChat          = "/v1/chat/completions"
	endpointResponses     = "/v1/responses"
	endpointMessages      = "/v1/messages"
	endpointModels        = "/v1/models"
	endpointBalance       = "/v1/dashboard/balance"
	endpointKeyInfo       = "/v1/key"
	endpointIssuerBalance = "/balance"

	testProviderID     = "wokey"
	testIssuerPrefix   = "wkt_"
	testKeyIssuerToken = "issuer_token"
)

// testAdapter is the Wokey-shaped provider of the service tests.
type testAdapter struct {
	catalog    *testCatalog
	issuerBase string
}

var (
	_ provider.Adapter             = (*testAdapter)(nil)
	_ provider.ErrorRemapper       = (*testAdapter)(nil)
	_ provider.CredentialExtension = (*testAdapter)(nil)
	_ provider.AccountViewer       = (*testAdapter)(nil)
	_ provider.QuotaChecker        = (*testAdapter)(nil)
)

func newTestAdapter() *testAdapter {
	return &testAdapter{catalog: &testCatalog{snapshot: testSnapshot{byID: map[string]testModel{}}}}
}

func (a *testAdapter) Spec() provider.Spec {
	return provider.Spec{
		ID:          testProviderID,
		DisplayName: "Wokey",
		Author:      "berry-shake",
		Repository:  "https://github.com/berry-shake/cpa-wokey-plugin",
		Config: config.Profile{
			BaseURL:         "https://api.wokey.ai",
			ModelPrefix:     testProviderID,
			DataDir:         "plugins/wokey-data",
			Database:        "wokey.db",
			BaseURLSuffixes: []string{"/v1"},
		},
		Page: func() []byte { return []byte("<!doctype html><html><title>test</title></html>") },
		Logo: func() []byte { return []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`) },
		Texts: provider.Texts{
			LoginStart: "Wokey uses API keys",
			QuotaRoute: "Balance and key usage.",
		},
		ForwardHeaders:     []string{"X-End-User-Id"},
		CountTokensRefusal: "token counting is not supported",
	}
}

func (a *testAdapter) Endpoint(protocol provider.Protocol) string {
	switch protocol {
	case provider.Responses:
		return endpointResponses
	case provider.Messages:
		return endpointMessages
	default:
		return endpointChat
	}
}

func (a *testAdapter) Catalog() provider.CatalogSource { return a.catalog }

func (a *testAdapter) VerifyKey(ctx context.Context, env provider.Env, client *http.Client, cred credential.Credential) *pluginabi.Error {
	_, perr := env.Errors.GetJSON(ctx, client, env.BaseURL(cred)+endpointKeyInfo, cred.APIKey, env.UserAgent, endpointKeyInfo)
	return perr
}

// AccountKey is empty: like Wokey's, the answers are not cached.
func (a *testAdapter) AccountKey(provider.Env, credential.Credential) string { return "" }

func (a *testAdapter) FetchAccount(env provider.Env, client *http.Client, cred credential.Credential) (any, bool, *pluginabi.Error) {
	return a.fetchAccount(env, client, cred, false)
}

func (a *testAdapter) FetchAccountView(env provider.Env, client *http.Client, cred credential.Credential) (any, bool, *pluginabi.Error) {
	return a.fetchAccount(env, client, cred, true)
}

// testAccount is what the test provider reports about a key.
type testAccount struct {
	Balance      *testBalance `json:"balance,omitempty"`
	BalanceError string       `json:"balance_error,omitempty"`
	Key          *testKeyInfo `json:"key,omitempty"`
	KeyError     string       `json:"key_error,omitempty"`
	Issuer       *testIssuer  `json:"issuer,omitempty"`
	IssuerError  string       `json:"issuer_error,omitempty"`
	FetchedAt    string       `json:"fetched_at"`
}

type testBalance struct {
	Account      string  `json:"account,omitempty"`
	AvailableUSD float64 `json:"available_usd"`
	ReservedUSD  float64 `json:"reserved_usd"`
}

type testKeyInfo struct {
	Label           string   `json:"label,omitempty"`
	UsageUSD        float64  `json:"usage_usd"`
	MonthlyUSD      float64  `json:"monthly_usd"`
	LimitUSD        *float64 `json:"limit_usd,omitempty"`
	LimitRemaining  *float64 `json:"limit_remaining_usd,omitempty"`
	LimitResetLabel string   `json:"limit_reset,omitempty"`
}

type testIssuer struct {
	Account     string   `json:"account"`
	ProviderUSD *float64 `json:"provider_usd,omitempty"`
	ReferrerUSD float64  `json:"referrer_usd"`
}

func (a *testAdapter) fetchAccount(env provider.Env, client *http.Client, cred credential.Credential, withIssuer bool) (testAccount, bool, *pluginabi.Error) {
	get := func(path string) (map[string]any, *pluginabi.Error) {
		ctx, cancel := context.WithTimeout(env.Context, provider.AccountCallTimeout)
		defer cancel()
		return env.Errors.GetJSON(ctx, client, env.BaseURL(cred)+path, cred.APIKey, env.UserAgent, path)
	}
	balanceRaw, balanceErr := get(endpointBalance)
	keyRaw, keyErr := get(endpointKeyInfo)
	info := testAccount{}
	if token := issuerTokenOf(cred); withIssuer && token != "" {
		ctx, cancel := context.WithTimeout(env.Context, provider.AccountCallTimeout)
		issuer, perr := a.issuerBalance(ctx, env, client, token)
		cancel()
		info.Issuer = issuer
		if perr != nil {
			info.IssuerError = perr.Message
		}
	}
	info.FetchedAt = env.Now().UTC().Format(time.RFC3339)
	if balanceErr != nil {
		info.BalanceError = balanceErr.Message
	} else {
		info.Balance = &testBalance{
			AvailableUSD: upstream.NumberField(balanceRaw, "availableUsd", "available_usd"),
			ReservedUSD:  upstream.NumberField(balanceRaw, "reservedUsd", "reserved_usd"),
		}
		if user := jsonfield.Text(balanceRaw["userId"]); user != "" {
			info.Balance.Account = "user:" + user
		}
	}
	if keyErr != nil {
		info.KeyError = keyErr.Message
	} else {
		info.Key = &testKeyInfo{Label: jsonfield.String(keyRaw, "label"), UsageUSD: upstream.NumberField(keyRaw, "usage"), MonthlyUSD: upstream.NumberField(keyRaw, "usage_monthly"), LimitResetLabel: jsonfield.String(keyRaw, "limit_reset")}
		if limit, ok := upstream.OptionalNumber(keyRaw["limit"]); ok {
			info.Key.LimitUSD = &limit
		}
		if remaining, ok := upstream.OptionalNumber(keyRaw["limit_remaining"]); ok {
			info.Key.LimitRemaining = &remaining
		}
	}
	if balanceErr != nil && keyErr != nil {
		return info, false, keyErr
	}
	return info, balanceErr == nil && keyErr == nil, nil
}

func (a *testAdapter) issuerBalance(ctx context.Context, env provider.Env, client *http.Client, token string) (*testIssuer, *pluginabi.Error) {
	if !validTestIssuerToken(token) {
		return nil, upstream.RequestError("invalid_issuer_token", "wokey: issuer_token must start with "+testIssuerPrefix)
	}
	raw, perr := env.Errors.GetJSON(ctx, client, a.issuerBase+endpointIssuerBalance, token, env.UserAgent, endpointIssuerBalance)
	if perr != nil {
		return nil, perr
	}
	info := &testIssuer{Account: credential.Fingerprint(token)}
	if fields, ok := raw["provider"].(map[string]any); ok {
		earnings := upstream.NumberField(fields, "availableUsd")
		info.ProviderUSD = &earnings
	}
	if fields, ok := raw["referrer"].(map[string]any); ok {
		info.ReferrerUSD = upstream.NumberField(fields, "availableUsd")
	}
	return info, nil
}

func (a *testAdapter) QuotaError(info any) *pluginabi.Error {
	account, _ := info.(testAccount)
	switch {
	case account.Balance == nil:
		return &pluginabi.Error{Code: "balance_unavailable", Message: "wokey: read balance: " + account.BalanceError, HTTPStatus: http.StatusBadGateway}
	case account.Key == nil:
		return &pluginabi.Error{Code: "usage_unavailable", Message: "wokey: read key usage: " + account.KeyError, HTTPStatus: http.StatusBadGateway}
	}
	return nil
}

// Quota draws one monthly bar: the balance against the balance plus this
// month's spend.
func (a *testAdapter) Quota(info any) pluginapi.QuotaFetchResponse {
	account, _ := info.(testAccount)
	var out pluginapi.QuotaFetchResponse
	if account.Balance == nil || account.Key == nil {
		return out
	}
	balance := math.Max(0, account.Balance.AvailableUSD)
	fraction := 0.0
	if start := balance + math.Max(0, account.Key.MonthlyUSD); start > 0 {
		fraction = balance / start
	}
	out.Groups = []pluginapi.QuotaGroup{{DisplayName: "Wokey", Buckets: []pluginapi.QuotaBucket{{Window: "monthly", RemainingFraction: fraction}}}}
	return out
}

func (a *testAdapter) RemapError(status int, code, message string) (int, string) {
	if status == http.StatusForbidden && (code == "request_blocked" || code == "model_denylisted") {
		return http.StatusBadRequest, message
	}
	return 0, message
}

func issuerTokenOf(cred credential.Credential) string {
	return jsonfield.String(cred.Fields, testKeyIssuerToken, "issuer-token")
}

func validTestIssuerToken(token string) bool {
	return len(token) > len(testIssuerPrefix) && strings.HasPrefix(token, testIssuerPrefix) && !strings.ContainsAny(token, " \t\r\n")
}

type testCredentialInput struct {
	IssuerToken *string `json:"issuer_token"`
}

func (a *testAdapter) DecodeCredentialInput(body []byte) (any, error) {
	var input testCredentialInput
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return input, nil
}

func (a *testAdapter) ApplyCredentialInput(fields map[string]any, decoded any) *provider.InputError {
	input, _ := decoded.(testCredentialInput)
	if input.IssuerToken == nil {
		return nil
	}
	token := strings.TrimSpace(*input.IssuerToken)
	if token != "" && !validTestIssuerToken(token) {
		return &provider.InputError{Code: "invalid_issuer_token", Message: "issuer_token must be a Wokey issuing token starting with " + testIssuerPrefix}
	}
	setOrDelete(fields, testKeyIssuerToken, token)
	return nil
}

func (a *testAdapter) CheckCredential(cred credential.Credential, previous *credential.Credential) *provider.CredentialCheck {
	token := issuerTokenOf(cred)
	if token == "" || (previous != nil && token == issuerTokenOf(*previous)) {
		return nil
	}
	return &provider.CredentialCheck{
		Code: "issuer_token_verification_failed",
		Run: func(ctx context.Context, env provider.Env, client *http.Client) *pluginabi.Error {
			_, perr := a.issuerBalance(ctx, env, client, token)
			return perr
		},
	}
}

func (a *testAdapter) CredentialView(cred credential.Credential) []provider.ViewMember {
	token := issuerTokenOf(cred)
	if token == "" {
		return nil
	}
	return []provider.ViewMember{{Name: "issuer_token_masked", Value: credential.MaskKey(token), Before: "prefix"}}
}

// testModel is one model of the test catalog (Wokey's /v1/models).
type testModel struct {
	ID                  string            `json:"id"`
	OwnedBy             string            `json:"owned_by"`
	Name                string            `json:"name"`
	ContextLength       int64             `json:"context_length"`
	MaxCompletionTokens int64             `json:"max_completion_tokens"`
	Pricing             map[string]string `json:"pricing"`
}

type testSnapshot struct {
	models    []testModel
	byID      map[string]testModel
	revision  string
	etag      string
	fetchedAt time.Time
	checkedAt time.Time
	lastError string
}

func (c testSnapshot) Summary() provider.CatalogSummary {
	return provider.CatalogSummary{Count: len(c.models), Revision: c.revision, FetchedAt: c.fetchedAt, CheckedAt: c.checkedAt, LastError: c.lastError}
}

func (c testSnapshot) Has(id string) bool {
	_, ok := c.byID[id]
	return ok
}

func (c testSnapshot) ModelInfos(all bool, ids []string) []pluginapi.ModelInfo {
	selected := c.models
	if !all {
		selected = make([]testModel, 0, len(ids))
		for _, id := range ids {
			if model, ok := c.byID[id]; ok {
				selected = append(selected, model)
				continue
			}
			selected = append(selected, testModel{ID: id})
		}
	}
	out := make([]pluginapi.ModelInfo, 0, len(selected))
	for _, model := range selected {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		out = append(out, pluginapi.ModelInfo{ID: model.ID, Object: "model", OwnedBy: testProviderID, Type: testProviderID, DisplayName: name, Name: model.ID,
			ContextLength: model.ContextLength, MaxCompletionTokens: model.MaxCompletionTokens, UserDefined: true})
	}
	return out
}

// UpstreamID drops the prefix, or any unknown one, like Wokey.
func (c testSnapshot) UpstreamID(model, prefix string) string {
	model = strings.TrimSpace(model)
	if prefix != "" && strings.HasPrefix(model, prefix+"/") {
		return strings.TrimPrefix(model, prefix+"/")
	}
	if c.Has(model) {
		return model
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

// ModelsView shows prices in USD per million tokens.
func (c testSnapshot) ModelsView() any {
	type view struct {
		ID      string              `json:"id"`
		Name    string              `json:"name,omitempty"`
		Pricing map[string]*float64 `json:"pricing"`
	}
	perMillion := func(raw string) *float64 {
		value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if errParse != nil {
			return nil
		}
		value *= 1_000_000
		return &value
	}
	out := make([]view, 0, len(c.models))
	for _, model := range c.models {
		pricing := map[string]*float64{}
		for name, key := range map[string]string{"input": "prompt", "output": "completion", "cache_read": "input_cache_read"} {
			if price := perMillion(model.Pricing[key]); price != nil {
				pricing[name] = price
			}
		}
		out = append(out, view{ID: model.ID, Name: model.Name, Pricing: pricing})
	}
	return out
}

// testCatalog loads /v1/models with If-None-Match, like Wokey's catalog.
type testCatalog struct {
	mu       sync.Mutex
	snapshot testSnapshot
}

func (c *testCatalog) Current() provider.Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

func (c *testCatalog) Fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (provider.Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fail := func(err error) (provider.Catalog, error) {
		c.snapshot.checkedAt, c.snapshot.lastError = now, err.Error()
		return c.snapshot, err
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+endpointModels, nil)
	if errReq != nil {
		return c.snapshot, errReq
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if !force && c.snapshot.etag != "" && len(c.snapshot.models) > 0 {
		req.Header.Set("If-None-Match", c.snapshot.etag)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return fail(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		c.snapshot.checkedAt, c.snapshot.lastError = now, ""
		return c.snapshot, nil
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return fail(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("GET /v1/models returned %d: %s", resp.StatusCode, upstream.Truncate(string(body), 300)))
	}
	var list struct {
		Data []testModel `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
		return fail(fmt.Errorf("decode /v1/models: %w", errUnmarshal))
	}
	byID := map[string]testModel{}
	models := make([]testModel, 0, len(list.Data))
	for _, model := range list.Data {
		if _, seen := byID[model.ID]; model.ID == "" || seen {
			continue
		}
		byID[model.ID] = model
		models = append(models, model)
	}
	if len(models) == 0 {
		return fail(fmt.Errorf("/v1/models returned no models"))
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	hash := sha256.New()
	for _, model := range models {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\n", model.ID, model.Name, model.ContextLength, model.MaxCompletionTokens)
	}
	c.snapshot = testSnapshot{models: models, byID: byID, revision: hex.EncodeToString(hash.Sum(nil))[:16], etag: strings.TrimSpace(resp.Header.Get("ETag")), fetchedAt: now, checkedAt: now}
	return c.snapshot, nil
}

// The test provider's routes and the request ID header, as the plugin
// package named them.
var (
	testPaths      = pathsFor(testProviderID)
	managementBase = "/v0/management/plugins/" + testProviderID
	resourceBase   = "/v0/resource/plugins/" + testProviderID

	logoPath, pagePath, flagPath                                      = testPaths.logo, testPaths.page, testPaths.flag
	statusPath, modelsPath, modelsRefresh, credentialsPath, quotaPath = testPaths.status, testPaths.models, testPaths.modelsRefresh, testPaths.credentials, testPaths.quota
	logsPath, proxiesPath, proxiesCheckPath, proxiesImportPath        = testPaths.logs, testPaths.proxies, testPaths.proxiesCheck, testPaths.proxiesImport
	proxiesOrderPath                                                  = testPaths.proxiesOrder
)

const cpaRequestIDHeader = "X-Wokey-Cpa-Request-Id"

// targetOf is the call target of an endpoint of the test provider.
func targetOf(endpoint string) target {
	switch endpoint {
	case endpointResponses:
		return target{protocol: provider.Responses, endpoint: endpoint}
	case endpointMessages:
		return target{protocol: provider.Messages, endpoint: endpoint}
	default:
		return target{protocol: provider.Chat, endpoint: endpoint}
	}
}
