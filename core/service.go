// Package core implements a CLIProxyAPI provider plugin around a
// provider.Adapter.
//
// Each API key of the provider is a CPA auth file
// ({"type":"<id>","api_key":...}). The service registers the key's models
// (all chat models of the catalog or a selected subset; CPA adds the auth
// prefix, "<id>/" by default), executes requests natively in the three
// protocols the providers serve (OpenAI chat completions and Responses,
// Anthropic Messages), reports the account behind a key through CPA's quota
// panel and serves a management page to add keys, pick models and manage
// egress proxies. The adapter supplies what differs between providers:
// endpoints, the catalog, the account queries and a few texts.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/proxystore"
	"github.com/berry-shake/cpa-provider-core/requestlog"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// catalogLoadTimeout bounds the synchronous catalog load in model.for_auth.
const catalogLoadTimeout = 15 * time.Second

// maxSchemaVersion is the newest RPC contract implemented here. Schema 6
// returns management JSON verbatim; schema 7 lets the request interceptor
// skip request bodies. It is pinned so an SDK bump cannot silently opt into
// newer contracts.
const maxSchemaVersion = pluginabi.SchemaVersionRequestInterceptorOmitBody

// HostCaller invokes a CPA host callback and returns the raw response envelope.
type HostCaller func(method string, request []byte) ([]byte, error)

// Options are the seams the snapshot tests (package coretest) use; the
// plugins build their service with New.
type Options struct {
	// Now replaces the service clock.
	Now func() time.Time
	// ManualRefresh keeps plugin.register from starting the catalog refresh
	// loop; a queued refresh then waits for RunQueuedRefresh.
	ManualRefresh bool
	// EgressCheckURLs replaces the IP lookup services of the egress check.
	EgressCheckURLs []string
}

// Service dispatches plugin RPC methods.
type Service struct {
	adapter   provider.Adapter
	spec      provider.Spec
	catalog   provider.CatalogSource
	decorator provider.HeaderDecorator
	counter   provider.TokenCounter
	// The other optional interfaces of the adapter; nil when not implemented.
	credExt      provider.CredentialExtension
	viewer       provider.AccountViewer
	quotaChecker provider.QuotaChecker
	fixer        provider.StreamFixer
	quotaFetcher provider.QuotaFetcher
	quotaPage    provider.QuotaPage
	// keyed is the catalog when it keeps one model list per key
	// (provider.KeyedCatalogSource), nil otherwise.
	keyed provider.KeyedCatalogSource
	// routes are the adapter's own management routes
	// (provider.ManagementExtension).
	routes []provider.ManagementRoute
	errors upstream.Errors
	paths  paths
	// requestMembers are the top-level request members prepareCall reads or
	// edits; the rest of the body is passed through without being decoded.
	requestMembers []string
	// forwardHeaders are the client headers prepareCall passes on.
	forwardHeaders []string
	// requestIDHeader carries CPA's request ID from the interceptor to the
	// executor (see interceptor.go).
	requestIDHeader string

	version string
	host    HostCaller
	now     func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.RWMutex
	cfg       config.Config
	hostProxy string
	// hostForcePrefix mirrors CPA's routing.force-model-prefix; hostKnown
	// is false until a request carrying the host summary arrives.
	hostForcePrefix bool
	hostKnown       bool

	clients *egress.Clients
	streams streamGroup
	// accountQueue sends account queries (balance, usage, key checks) out
	// one at a time per egress.
	accountQueue *egress.Queue
	// accounts caches account queries per key; see fetchAccount.
	accounts accountCache
	// proxies is the proxy list of the management page.
	proxies *proxystore.Store
	// egressCheckURLs are the IP lookup services of the egress check.
	egressCheckURLs []string

	loopOnce sync.Once
	kick     chan struct{}
	// registered maps auth ID -> catalog revision its models were built from
	// ("" when built without a catalog), so catalog changes can be pushed to
	// CPA by re-saving only the affected auth files.
	registered sync.Map
	// authIDs holds the CPA auth IDs seen as this provider's credentials, so
	// the request interceptor only touches requests routed to this plugin.
	authIDs sync.Map
	reqlog  *requestlog.Log
}

// New builds the service of a provider plugin. host may be nil in tests.
func New(adapter provider.Adapter, version string, host HostCaller) *Service {
	return NewWithOptions(adapter, version, host, Options{})
}

// NewWithOptions is New with test seams.
func NewWithOptions(adapter provider.Adapter, version string, host HostCaller, opts Options) *Service {
	if strings.TrimSpace(version) == "" {
		version = "0.0.0-dev"
	}
	spec := adapter.Spec()
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		adapter:         adapter,
		spec:            spec,
		catalog:         adapter.Catalog(),
		errors:          upstream.Errors{Prefix: spec.ID + ": "},
		paths:           pathsFor(spec.ID),
		requestMembers:  append(append([]string(nil), coreRequestMembers...), spec.RequestMembers...),
		forwardHeaders:  append(append([]string(nil), forwardedRequestHeaders...), spec.ForwardHeaders...),
		requestIDHeader: "X-" + spec.DisplayName + "-Cpa-Request-Id",

		version:      version,
		host:         host,
		now:          time.Now,
		ctx:          ctx,
		cancel:       cancel,
		cfg:          config.Default(spec.Config),
		clients:      egress.NewClients(),
		accountQueue: egress.NewQueue(),
		kick:         make(chan struct{}, 1),

		proxies:         proxystore.New(),
		egressCheckURLs: defaultEgressCheckURLs,
	}
	svc.decorator, _ = adapter.(provider.HeaderDecorator)
	svc.counter, _ = adapter.(provider.TokenCounter)
	svc.credExt, _ = adapter.(provider.CredentialExtension)
	svc.viewer, _ = adapter.(provider.AccountViewer)
	svc.quotaChecker, _ = adapter.(provider.QuotaChecker)
	svc.fixer, _ = adapter.(provider.StreamFixer)
	svc.quotaFetcher, _ = adapter.(provider.QuotaFetcher)
	svc.quotaPage, _ = adapter.(provider.QuotaPage)
	svc.keyed, _ = svc.catalog.(provider.KeyedCatalogSource)
	if ext, ok := adapter.(provider.ManagementExtension); ok {
		svc.routes = ext.ManagementRoutes()
	}
	if remapper, ok := adapter.(provider.ErrorRemapper); ok {
		svc.errors.Remap = remapper.RemapError
	}
	if opts.Now != nil {
		svc.now = opts.Now
	}
	if opts.ManualRefresh {
		svc.loopOnce.Do(func() {})
	}
	if opts.EgressCheckURLs != nil {
		svc.egressCheckURLs = append([]string(nil), opts.EgressCheckURLs...)
	}
	svc.reqlog = requestlog.New(spec.Config.Database, svc.errors.Prefix, func(message string) { svc.logf("warn", message, nil) })
	if user, ok := adapter.(provider.HostUser); ok {
		user.Attach(serviceHost{svc})
	}
	return svc
}

// Close stops background work and in-flight streams.
func (s *Service) Close() {
	s.cancel()
	s.streams.waitTimeout(3 * time.Second)
	s.clients.CloseIdle()
	s.reqlog.Close()
}

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcCapabilities struct {
	AuthProvider          bool     `json:"auth_provider"`
	ModelProvider         bool     `json:"model_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool     `json:"management_api"`
	QuotaProvider         bool     `json:"quota_provider"`
	// RequestInterceptor only reads headers and metadata, so both phases
	// opt out of the request body.
	RequestInterceptor               bool `json:"request_interceptor,omitempty"`
	RequestInterceptorOmitBodyBefore bool `json:"request_interceptor_omit_body_before,omitempty"`
	RequestInterceptorOmitBodyAfter  bool `json:"request_interceptor_omit_body_after,omitempty"`
}

type rpcRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  rpcCapabilities    `json:"capabilities"`
}

type rpcIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// Call handles one plugin RPC method and always returns a JSON envelope.
func (s *Service) Call(method string, request []byte) []byte {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(s.register(request))
	case pluginabi.MethodPluginQuiesce:
		return okEnvelope(struct{}{})
	case pluginabi.MethodPluginShutdown:
		s.Close()
		return okEnvelope(struct{}{})

	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier:
		return okEnvelope(rpcIdentifierResponse{Identifier: s.spec.ID})
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode auth parse request: "+errUnmarshal.Error())
		}
		s.rememberHost(req.Host)
		resp, errParse := credential.ParseAuthFile(s.spec.ID, req, s.now(), s.config().ModelPrefix)
		if errParse != nil {
			return ErrorEnvelope("invalid_auth", errParse.Error())
		}
		return okEnvelope(resp)
	case pluginabi.MethodAuthLoginStart:
		return errorEnvelopeFrom(&pluginabi.Error{
			Code:       "unsupported_login",
			Message:    s.spec.Texts.LoginStart,
			HTTPStatus: http.StatusBadRequest,
		})
	case pluginabi.MethodAuthLoginPoll:
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatus("error"), Message: s.spec.DisplayName + " uses API keys; there is no login flow"})
	case pluginabi.MethodAuthRefresh:
		var req rpcAuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode auth refresh request: "+errUnmarshal.Error())
		}
		return s.refreshAuth(req.AuthRefreshRequest)

	case pluginabi.MethodModelStatic:
		return okEnvelope(pluginapi.ModelResponse{Provider: s.spec.ID})
	case pluginabi.MethodModelForAuth:
		var req rpcAuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode model request: "+errUnmarshal.Error())
		}
		return okEnvelope(s.modelsForAuth(req.AuthModelRequest))

	case pluginabi.MethodExecutorExecute:
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.execute(req.ExecutorRequest)
	case pluginabi.MethodExecutorExecuteStream:
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.executeStream(req)
	case pluginabi.MethodExecutorCountTokens:
		if s.counter == nil {
			// Refused before the request is decoded: there is nothing to
			// check it against.
			return errorEnvelopeFrom(&pluginabi.Error{
				Code:       "unsupported_operation",
				Message:    s.errors.Prefix + "count_tokens is not supported by " + s.spec.DisplayName,
				HTTPStatus: http.StatusNotImplemented,
			})
		}
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.countTokens(req.ExecutorRequest)
	case pluginabi.MethodExecutorHTTPRequest:
		return errorEnvelopeFrom(&pluginabi.Error{Code: "unsupported_operation", Message: s.errors.Prefix + "raw HTTP requests are not supported", HTTPStatus: http.StatusNotImplemented})

	case pluginabi.MethodManagementRegister:
		return okEnvelope(s.managementRoutes())
	case pluginabi.MethodManagementHandle:
		var req rpcManagementRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode management request: "+errUnmarshal.Error())
		}
		return okEnvelope(s.handleManagement(req.ManagementRequest))

	case pluginabi.MethodRequestInterceptBefore:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		var req rpcRequestInterceptRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode request intercept request: "+errUnmarshal.Error())
		}
		return okEnvelope(s.interceptAfterAuth(req.RequestInterceptRequest))

	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{SupportedProviders: []string{s.spec.ID}, DisplayName: s.spec.DisplayName})
	case pluginabi.MethodQuotaFetch:
		var req rpcQuotaFetchRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode quota request: "+errUnmarshal.Error())
		}
		return s.quotaFetch(req.QuotaFetchRequest)
	case pluginabi.MethodQuotaReset:
		// The providers' allowances cannot be reset from here. CPA reads
		// success false as a refused reset and shows the message.
		return okEnvelope(pluginapi.QuotaResetResponse{Success: false, Message: s.errors.Prefix + "quota reset is not supported"})

	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method)
	}
}

func (s *Service) register(request []byte) rpcRegistration {
	var lifecycle rpcLifecycleRequest
	_ = json.Unmarshal(request, &lifecycle)
	cfg, warnings := config.Parse(lifecycle.ConfigYAML, s.spec.Config)
	s.mu.Lock()
	previous := s.cfg
	s.cfg = cfg
	s.mu.Unlock()
	for _, warning := range warnings {
		s.logf("warn", s.errors.Prefix+warning, nil)
	}
	s.reqlog.Configure(cfg.DataDir, cfg.LogRetention, cfg.LogRetentionDays)
	s.proxies.Configure(cfg.DataDir)
	if _, loadErr := s.proxies.List(); loadErr != "" {
		s.logf("warn", s.errors.Prefix+loadErr, nil)
	}
	s.loopOnce.Do(func() { go s.refreshLoop() })
	if previous.ModelPrefix != cfg.ModelPrefix || previous.BaseURL != cfg.BaseURL {
		// The default auth prefix is applied when CPA parses an auth file,
		// so re-save every file to push the new prefix to CPA.
		s.registered.Range(func(key, _ any) bool {
			s.registered.Store(key, "stale")
			return true
		})
	}
	s.kickRefresh()
	schema := negotiateSchemaVersion(lifecycle.SchemaVersion)
	intercept := interceptsRequests(schema)
	formats := executorFormats()
	return rpcRegistration{
		SchemaVersion: schema,
		Metadata: pluginapi.Metadata{
			Name:             s.spec.DisplayName,
			Version:          s.version,
			Author:           s.spec.Author,
			GitHubRepository: s.spec.Repository,
			Logo:             s.paths.logo,
			ConfigFields:     config.Fields(s.spec.Config),
		},
		Capabilities: rpcCapabilities{
			AuthProvider:          true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  formats,
			ExecutorOutputFormats: formats,
			ManagementAPI:         true,
			QuotaProvider:         true,

			RequestInterceptor:               intercept,
			RequestInterceptorOmitBodyBefore: intercept,
			RequestInterceptorOmitBodyAfter:  intercept,
		},
	}
}

// negotiateSchemaVersion answers with the newest schema both sides support;
// a host that advertises nothing only knows schema 1.
func negotiateSchemaVersion(host uint32) uint32 {
	if host == 0 {
		return 1
	}
	return min(host, maxSchemaVersion)
}

func (s *Service) config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// env is what adapter calls get to work with.
func (s *Service) env() provider.Env {
	return provider.Env{Context: s.ctx, Now: s.now, UserAgent: s.userAgent(), Config: s.config(), Errors: s.errors}
}

// rememberHost caches CPA's global proxy-url and force-model-prefix, which
// arrive with auth and model requests.
func (s *Service) rememberHost(host pluginapi.HostConfigSummary) {
	proxyURL := strings.TrimSpace(host.ProxyURL)
	s.mu.Lock()
	s.hostProxy = proxyURL
	s.hostForcePrefix = host.ForceModelPrefix
	s.hostKnown = true
	s.mu.Unlock()
}

// forceModelPrefix reports CPA's routing.force-model-prefix and whether it
// is known yet.
func (s *Service) forceModelPrefix() (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostForcePrefix, s.hostKnown
}

func (s *Service) globalProxy() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostProxy
}

func (s *Service) userAgent() string {
	return "cpa-" + s.spec.ID + "-plugin/" + s.version
}

func (s *Service) refreshAuth(req pluginapi.AuthRefreshRequest) []byte {
	s.rememberHost(req.Host)
	s.noteAuth(req.AuthID)
	_, merged, errCred := credential.FromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil {
		return ErrorEnvelope("invalid_auth", errCred.Error())
	}
	raw, _ := json.Marshal(merged)
	auth, errAuth := credential.AuthData(s.spec.ID, merged, raw, "", s.now(), s.config().ModelPrefix)
	if errAuth != nil {
		return ErrorEnvelope("invalid_auth", errAuth.Error())
	}
	auth.ID = req.AuthID
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: auth.NextRefreshAfter})
}

// modelsForAuth lists the CPA models for one credential.
func (s *Service) modelsForAuth(req pluginapi.AuthModelRequest) pluginapi.ModelResponse {
	s.rememberHost(req.Host)
	s.noteAuth(req.AuthID)
	cred, _, errCred := credential.FromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil {
		s.logf("warn", s.errors.Prefix+"cannot read credential for model registration", map[string]any{"auth_id": req.AuthID, "error": errCred.Error()})
		return pluginapi.ModelResponse{Provider: s.spec.ID}
	}
	snapshot := s.catalogFor(cred)
	models := modelInfos(snapshot, cred)
	if req.AuthID != "" {
		s.registered.Store(req.AuthID, revisionFor(snapshot, cred))
	}
	return pluginapi.ModelResponse{Provider: s.spec.ID, Models: models}
}

// hostCall invokes a host callback and decodes its result into out (if non-nil).
func (s *Service) hostCall(method string, request any, out any) error {
	if s.host == nil {
		return fmt.Errorf("host callbacks are unavailable")
	}
	raw, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return errMarshal
	}
	resp, errCall := s.host(method, raw)
	if errCall != nil {
		return errCall
	}
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(resp, &envelope); errUnmarshal != nil {
		return fmt.Errorf("decode %s response: %w", method, errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return fmt.Errorf("%s: %s", method, envelope.Error.Message)
		}
		return fmt.Errorf("%s failed", method)
	}
	if out != nil && len(envelope.Result) > 0 {
		if errResult := json.Unmarshal(envelope.Result, out); errResult != nil {
			return fmt.Errorf("decode %s result: %w", method, errResult)
		}
	}
	return nil
}

type rpcHostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// logf writes to CPA's log. Fields must never contain API keys or proxy
// credentials.
func (s *Service) logf(level, message string, fields map[string]any) {
	_ = s.hostCall(pluginabi.MethodHostLog, rpcHostLogRequest{Level: level, Message: message, Fields: fields}, nil)
}

func okEnvelope(result any) []byte {
	rawResult, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return ErrorEnvelope("marshal_failed", errMarshal.Error())
	}
	raw, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: rawResult})
	return raw
}

// ErrorEnvelope builds a failure envelope for RPC and ABI initialization errors.
func ErrorEnvelope(code, message string) []byte {
	return errorEnvelopeFrom(&pluginabi.Error{Code: code, Message: message})
}

func errorEnvelopeFrom(err *pluginabi.Error) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: err})
	return raw
}
