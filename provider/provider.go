// Package provider is the contract between cpa-provider-core and a provider
// plugin: what the plugin declares about itself (Spec) and what the core asks
// it for (Adapter and the optional interfaces). Everything else — the RPC
// dispatch, credentials, the catalog sync, executing and streaming requests,
// the request log, proxies and the management page — lives in package core.
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/payload"
	"github.com/berry-shake/cpa-provider-core/requestlog"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Protocol is one of the inference protocols the plugins pass through
// untranslated. Its value is the CPA executor format declared for it.
type Protocol string

const (
	// Chat is OpenAI chat completions.
	Chat Protocol = "chat-completions"
	// Responses is the OpenAI Responses API.
	Responses Protocol = "responses"
	// Messages is the Anthropic Messages API.
	Messages Protocol = "anthropic"
)

// Protocols are the protocols in the order the executor formats are declared.
var Protocols = []Protocol{Chat, Responses, Messages}

// ProtocolForFormat maps a CPA executor format to its protocol.
func ProtocolForFormat(format string) (Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return Chat, true
	case "openai-response", "openai-responses", "responses":
		return Responses, true
	case "claude", "anthropic":
		return Messages, true
	default:
		return "", false
	}
}

// Spec is what a provider declares about itself. The IDs, names and texts end
// up in CPA's plugin list, the management routes, error messages and logs;
// strings the plugins word the same way are derived from ID and DisplayName
// by the core.
type Spec struct {
	// ID is the plugin ID, the CPA provider key and the auth file type
	// ("ollama"). It also prefixes error and log messages ("ollama: ") and
	// names the User-Agent ("cpa-ollama-plugin/<version>").
	ID string
	// DisplayName is the provider's name in texts and the menu ("Ollama").
	DisplayName string
	// Author and Repository fill the plugin metadata.
	Author     string
	Repository string
	// Config holds the config defaults and help texts.
	Config config.Profile
	// Page is the management page HTML and Logo the plugin logo (SVG).
	Page func() []byte
	Logo func() []byte
	// Texts are the provider-specific texts.
	Texts Texts
	// RequestMembers are top-level request members Decorate reads besides
	// the ones the core reads (model, stream_options and the reasoning
	// settings). They change where the request scan stops, so they are
	// only listed by providers that need them.
	RequestMembers []string
	// ForwardHeaders are client request headers passed on to the upstream
	// besides Anthropic-Beta and Anthropic-Version (Wokey's X-End-User-Id).
	// Client credentials must never be listed.
	ForwardHeaders []string
}

// Texts are the texts each provider words its own way.
type Texts struct {
	// LoginStart is the message of the auth.login.start error.
	LoginStart string
	// The descriptions of the management routes that differ between
	// providers.
	ModelsRoute         string
	ModelsRefreshRoute  string
	AddCredentialRoute  string
	EditCredentialRoute string
	QuotaRoute          string
}

// Env is what the core hands an adapter call: the service lifetime, its
// clock, User-Agent, config and error mapping.
type Env struct {
	// Context ends when the plugin shuts down.
	Context context.Context
	// Now is the service clock.
	Now func() time.Time
	// UserAgent is sent on every upstream request.
	UserAgent string
	// Config is the current plugin config.
	Config config.Config
	// Errors maps upstream answers to plugin errors.
	Errors upstream.Errors
}

// BaseURL is the base URL a credential's requests go to.
func (e Env) BaseURL(cred credential.Credential) string {
	return cred.EffectiveBaseURL(e.Config)
}

// AccountCallTimeout bounds one account request (balance, usage, key check).
// Waiting for the egress's turn does not count, except for the key check,
// which bounds the whole check.
const AccountCallTimeout = 20 * time.Second

// Adapter is a provider plugin as the core sees it. The core builds one
// Service per adapter; an adapter holds its own catalog state.
type Adapter interface {
	// Spec describes the provider. It is read once, when the service is
	// built.
	Spec() Spec
	// Endpoint is the path, relative to the base URL, a protocol is served
	// on ("/v1/chat/completions"). It is also what the request log shows.
	Endpoint(protocol Protocol) string
	// Catalog is the provider's model catalog.
	Catalog() CatalogSource

	// VerifyKey checks a key being added on the management page, in the
	// key's egress turn; client goes out through that egress. ctx bounds the
	// whole check (AccountCallTimeout, the wait for the turn included).
	VerifyKey(ctx context.Context, env Env, client *http.Client, cred credential.Credential) *pluginabi.Error
	// AccountKey identifies the account answer for a credential in the
	// account cache (a key fingerprint and the URL it is read from). An
	// empty key keeps the answers out of the cache.
	AccountKey(env Env, cred credential.Credential) string
	// FetchAccount reads the account behind a key (balance, usage) in the
	// key's egress turn. A complete answer is cached for a minute. With a
	// non-nil error the info is discarded.
	FetchAccount(env Env, client *http.Client, cred credential.Credential) (info any, complete bool, perr *pluginabi.Error)
	// Quota turns an account answer into CPA's quota panel response.
	Quota(info any) pluginapi.QuotaFetchResponse
}

// CallInput is what Decorate gets about an executor request.
type CallInput struct {
	// Request is the executor request as CPA sent it.
	Request pluginapi.ExecutorRequest
	// Members are the top-level request members found by the scan (the
	// core's and Spec.RequestMembers); ScanErr reports a malformed body met
	// before the scan stopped.
	Members map[string]payload.Span
	ScanErr error
}

// HeaderDecorator is implemented by adapters that add their own headers to
// executor requests (OpenCode's X-Opencode-Session). Decorate runs after the
// core set its headers and before the Anthropic headers are settled.
type HeaderDecorator interface {
	Decorate(header http.Header, in CallInput)
}

// TokenCounter is implemented by adapters whose upstream counts Messages
// tokens. Without it executor.count_tokens answers 501 "<id>: count_tokens
// is not supported by <DisplayName>" without decoding the request.
type TokenCounter interface {
	// CountTokensEndpoint is the path count_tokens requests are forwarded
	// to. They are sent like Messages requests and not written to the
	// request log.
	CountTokensEndpoint() string
}

// ErrorRemapper is implemented by adapters that map some upstream error
// answers their own way. RemapError becomes the Remap of the service's
// upstream.Errors (Env.Errors), so it applies to every upstream error the
// plugin reports: executor answers, error events before a stream's first
// output, and the adapter's own Env.Errors calls. See upstream.Remap.
type ErrorRemapper interface {
	RemapError(status int, code, message string) (mapped int, newMessage string)
}

// CredentialExtension is implemented by adapters whose auth files carry
// fields of their own that the management page sets, checks and shows
// (Wokey's issuer_token). The core keeps them in the auth file it saves;
// they reach the adapter as credential.Credential.Fields.
type CredentialExtension interface {
	// DecodeCredentialInput reads the provider's members of a credential
	// body (POST and PATCH credentials) once the core has decoded its own.
	// An error rejects the body like a malformed one: 400 invalid_json with
	// the error's text.
	DecodeCredentialInput(body []byte) (input any, err error)
	// ApplyCredentialInput validates the decoded input and writes it into the
	// auth file's fields. It runs after base_url and proxy_url are applied
	// and before prefix, models and priority; a non-nil error rejects the
	// body with 400.
	ApplyCredentialInput(fields map[string]any, input any) *InputError
	// CheckCredential returns the check a credential saved with verification
	// needs besides VerifyKey, or nil. previous is the credential as it was
	// before an edit, nil when one is added (then the check runs after
	// VerifyKey passed).
	CheckCredential(cred credential.Credential, previous *credential.Credential) *CredentialCheck
	// CredentialView lists the members the credential's management view
	// adds (only for auth files that could be read).
	CredentialView(cred credential.Credential) []ViewMember
}

// InputError rejects a management request body: {"error":Code,"message":Message}.
type InputError struct {
	Code    string
	Message string
}

// CredentialCheck is a check of a credential's own fields. The core runs it
// in a turn of the key's egress; a failure answers the save with
// {"error":Code,"message":<the error's message>}, 400 when the upstream
// rejected the check (400, 401 or 403) and 502 otherwise.
type CredentialCheck struct {
	Code string
	// Run checks through client; ctx bounds the whole check
	// (AccountCallTimeout, the wait for the turn included).
	Run func(ctx context.Context, env Env, client *http.Client) *pluginabi.Error
}

// ViewMember is one member a provider adds to a JSON view. It goes before
// the member named Before, which must be one the view always has; empty
// Before puts it last.
type ViewMember struct {
	Name   string
	Value  any
	Before string
}

// AccountViewer is implemented by adapters whose management page shows more
// about an account than CPA's quota panel needs (Wokey adds the issuing
// balance of keys with an issuer token). The page's quota route then calls
// FetchAccountView instead of FetchAccount, the same way: in the key's egress
// turn, and cached under AccountKey plus " view" when complete.
type AccountViewer interface {
	FetchAccountView(env Env, client *http.Client, cred credential.Credential) (info any, complete bool, perr *pluginabi.Error)
}

// QuotaChecker is implemented by adapters that cannot draw CPA's quota panel
// from every account answer (Wokey needs both the balance and the key
// usage). quota.fetch answers with QuotaError's error, when not nil, instead
// of Quota.
type QuotaChecker interface {
	QuotaError(info any) *pluginabi.Error
}

// StreamFixer is implemented by adapters whose upstream sends stream events
// that need repair before they reach the client (OpenDesign's Messages
// streams). StreamFix is asked once per executor call, after the request
// scan; a nil StreamFix leaves the call's stream as it is.
type StreamFixer interface {
	StreamFix(protocol Protocol, in CallInput) StreamFix
}

// StreamFix repairs the SSE events of one stream (bodies that are not SSE
// are passed through untouched). The stream gate hands it every upstream
// event before reading it and reads what it returns instead, so the gate's
// hold-back, error and truncation rules and the request log see the repaired
// stream.
type StreamFix interface {
	// Event takes one upstream event and returns the events to read in its
	// place, in order; none holds it back.
	Event(event []byte) [][]byte
	// Flush returns the events still held back when the upstream ended the
	// stream without an error. The gate reads them as they are, not
	// through Event.
	Flush() [][]byte
	// Close returns the events that end a stream the upstream ended without
	// an error but also without its terminal event, after output had
	// started (and after Flush). They go through Event like upstream
	// events. None reports the stream truncated.
	Close() [][]byte
}

// KeyedCatalogSource is implemented by catalog sources that keep one model
// list per API key (OpenDesign: a key's plan decides which models it may
// use). The core then loads each key's list through the key's own egress
// and base URL instead of fetching one catalog:
//
//   - model.for_auth and adding a key load the key's list when the catalog
//     does not hold it yet (KeyedCatalog.KnowsKey); a key without api_key
//     loads nothing.
//   - A refresh reloads the list of every distinct key, drops the lists of
//     keys no auth file has any more (Retain, only when there are auth files
//     or lists), fails only when no list loaded, and re-registers each
//     credential whose models were built from an older RevisionFor.
//   - Registrations, model_revision and the status view's missing models
//     go by the key (KeyModelInfos, RevisionFor, KeyHas); the status
//     view's catalog summary adds "keys" (KeyedCatalog.Keys).
//
// Keys are named by credential.Fingerprint of the api_key. The source's
// Current must return a KeyedCatalog; its Fetch is not called.
type KeyedCatalogSource interface {
	CatalogSource
	// FetchKey reloads the list of one key through client. On error it
	// returns the snapshot it still has (with the error recorded in its
	// summary unless the request could not be built).
	FetchKey(ctx context.Context, client *http.Client, baseURL, apiKey, keyID, userAgent string, now time.Time) (Catalog, error)
	// Retain drops the lists of the keys outside keep and returns the
	// snapshot.
	Retain(keep map[string]bool) Catalog
}

// KeyedCatalog is a snapshot of a KeyedCatalogSource: the union of the
// keys' lists, which is what Has, UpstreamID and ModelsView read.
type KeyedCatalog interface {
	Catalog
	// KnowsKey reports whether the snapshot holds the key's list.
	KnowsKey(keyID string) bool
	// Keys counts the keys whose lists the snapshot holds.
	Keys() int
	// RevisionFor is the revision of what one key registers (the whole
	// catalog's while its list is not loaded), so a change to another key's
	// list does not re-register it. It is empty while the catalog is.
	RevisionFor(keyID string) string
	// KeyModelInfos is ModelInfos for one key: with all, the chat models
	// its list has (every chat model while it is not loaded).
	KeyModelInfos(all bool, ids []string, keyID string) []pluginapi.ModelInfo
	// KeyHas reports whether a key may register a model: the catalog has
	// it and, once the key's list is loaded, the list has it too.
	KeyHas(keyID, id string) bool
}

// HostUser is implemented by adapters that need more of the service than
// the calls above hand them: their own management routes, quota answers or
// background work (OpenDesign's account sign-in). The core calls Attach once,
// while the service is built, before any RPC.
type HostUser interface {
	Attach(host Host)
}

// Host is what the service lends a HostUser.
type Host interface {
	// Env is what adapter calls get: the service lifetime and clock, the
	// User-Agent, the current config and the error mapping.
	Env() Env
	// Logf writes to CPA's log. Messages start with the error prefix
	// ("<id>: "); fields must never hold keys or proxy credentials.
	Logf(level, message string, fields map[string]any)
	// AuthFiles lists the provider's credentials with their auth files, as
	// the status view sees them.
	AuthFiles() ([]AuthFile, error)
	// ReadAuthFile reads one auth file by auth index: its fields and the
	// name SaveAuthFile takes.
	ReadAuthFile(authIndex string) (fields map[string]any, name string, err error)
	// SaveAuthFile writes an auth file through CPA, which parses it again
	// and re-registers its models.
	SaveAuthFile(name string, fields map[string]any) error
	// Client returns the HTTP client of the egress a proxy_url resolves to
	// (the credential's proxy, CPA's global proxy, or the environment); the
	// error is the proxy_url's validation error as is.
	Client(proxyURL string) (*http.Client, error)
	// Usage sums the request log entries of one credential
	// (requestlog.Log.Usage).
	Usage(authIndex, authID string, now time.Time) (requestlog.Usage, string)
}

// AuthFile is one of the provider's credentials known to CPA with its auth
// file.
type AuthFile struct {
	pluginapi.HostAuthFileEntry
	// Fields and Credential are the auth file as read; both are empty when
	// ReadError says why it could not be read.
	Fields     map[string]any
	Credential credential.Credential
	ReadError  string
}

// ManagementExtension is implemented by adapters that serve management
// routes of their own (OpenDesign's account sign-in). They are declared
// after the quota route; a request for one of their paths with another
// method is answered 405 like the core's routes.
type ManagementExtension interface {
	ManagementRoutes() []ManagementRoute
}

// ManagementRoute is one management route an adapter serves.
type ManagementRoute struct {
	Method string
	// Path is relative to the plugin's management base
	// (/v0/management/plugins/<id>), for example "/account/login".
	Path        string
	Description string
	// Handle answers the route; request bodies are already bounded.
	Handle func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse
}

// QuotaFetcher is implemented by adapters that answer CPA's quota panel
// (quota.fetch) themselves instead of through FetchAccount, QuotaChecker and
// Quota (OpenDesign shows the plan windows of the account signed in on a
// key, read by auth index).
type QuotaFetcher interface {
	FetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error)
}

// QuotaPage is implemented by adapters that serve the management page's
// quota route (GET credentials/quota) themselves instead of through the
// account query (OpenDesign adds the usage summed from the request log).
type QuotaPage interface {
	ServeQuota(req pluginapi.ManagementRequest) pluginapi.ManagementResponse
}

// JSONResponse is a management answer with a JSON body, as the core's routes
// send it.
func JSONResponse(status int, v any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		status = http.StatusInternalServerError
		raw, _ = json.Marshal(ErrorBody{Error: "marshal_failed", Message: errMarshal.Error()})
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: raw,
	}
}

// ErrorBody is the body of a failed management request.
type ErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// CatalogSource is a provider's model catalog: the cached snapshot and how to
// reload it.
type CatalogSource interface {
	// Current returns the cached snapshot; it is never nil.
	Current() Catalog
	// Fetch reloads the catalog through client. With force the provider may
	// reload details it otherwise keeps. On error it returns the snapshot
	// it still has (with the error recorded in its summary).
	Fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (Catalog, error)
}

// Catalog is an immutable catalog snapshot.
type Catalog interface {
	// Summary describes the snapshot; Count 0 means no catalog is loaded.
	Summary() CatalogSummary
	// Has reports whether a model ID is in the catalog.
	Has(id string) bool
	// ModelInfos lists the CPA models of a credential: every chat model
	// when all is set, otherwise the selected IDs (unknown ones included).
	ModelInfos(all bool, ids []string) []pluginapi.ModelInfo
	// UpstreamID maps the model CPA hands the executor to the upstream ID.
	UpstreamID(model, prefix string) string
	// ModelsView is the model list of the management page's models view;
	// it must encode as a JSON array.
	ModelsView() any
}

// CatalogSummary describes a catalog snapshot on the management page.
type CatalogSummary struct {
	Count     int
	Revision  string
	FetchedAt time.Time
	CheckedAt time.Time
	LastError string
	// DetailErrors counts models whose details failed to load (Ollama).
	DetailErrors int
}
