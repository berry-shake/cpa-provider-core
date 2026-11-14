// Package provider is the contract between cpa-provider-core and a provider
// plugin: what the plugin declares about itself (Spec) and what the core asks
// it for (Adapter and the optional interfaces). Everything else — the RPC
// dispatch, credentials, the catalog sync, executing and streaming requests,
// the request log, proxies and the management page — lives in package core.
package provider

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/payload"
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

// StreamErrorTable picks the keyword table that tells which HTTP status an
// error event inside a stream stands for.
type StreamErrorTable int

const (
	// StreamErrorsBase is the table of Ollama and Wokey.
	StreamErrorsBase StreamErrorTable = iota
	// StreamErrorsExtended adds the words OpenCode and OpenDesign report
	// (budget and usage limits, insufficient funds, MissingSession, ...).
	StreamErrorsExtended
)

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
	// StreamErrors is the keyword table for errors inside a stream.
	StreamErrors StreamErrorTable
	// RequestMembers are top-level request members Decorate reads besides
	// the ones the core reads (model, stream_options and the reasoning
	// settings). They change where the request scan stops, so they are
	// only listed by providers that need them.
	RequestMembers []string
	// ForwardHeaders are client request headers passed on to the upstream
	// besides Anthropic-Beta and Anthropic-Version (Wokey's X-End-User-Id).
	// Client credentials must never be listed.
	ForwardHeaders []string
	// CountTokensRefusal, when set, answers executor.count_tokens with a 501
	// whose message is the error prefix and this text, before the request is
	// decoded (Wokey: "token counting is not supported"). Without it the
	// request is decoded first and then forwarded (TokenCounter) or refused
	// with "count_tokens is not supported by <DisplayName>".
	CountTokensRefusal string
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
// tokens. Without it executor.count_tokens answers 501.
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
