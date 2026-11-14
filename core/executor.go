package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/payload"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	defaultAnthropicVersion = "2023-06-01"

	// maxResponseBytes bounds a buffered (non-streaming) upstream response.
	maxResponseBytes = 64 << 20
)

// executorFormats are the protocols the providers serve natively. Declaring
// all three as both input and output lets CPA pass requests through
// untranslated.
func executorFormats() []string {
	out := make([]string, 0, len(provider.Protocols))
	for _, protocol := range provider.Protocols {
		out = append(out, string(protocol))
	}
	return out
}

// forwardedRequestHeaders are the client headers that change upstream
// behavior and are safe to pass on; adapters add theirs with
// Spec.ForwardHeaders. Client credentials are never forwarded: the key goes
// out only as a bearer token, so x-api-key is not sent either.
var forwardedRequestHeaders = []string{"Anthropic-Beta", "Anthropic-Version"}

// forwardedResponseHeaders are the upstream headers returned to CPA.
var forwardedResponseHeaders = []string{"Content-Type", "X-Request-Id", "Request-Id", "Retry-After"}

// coreRequestMembers are the top-level request members prepareCall reads or
// edits; adapters add theirs with Spec.RequestMembers.
var coreRequestMembers = []string{"model", "stream_options", "reasoning_effort", "reasoning", "output_config", "thinking"}

// target is where a call goes: the protocol it speaks and the endpoint path.
// countTokens marks a Messages count_tokens call, which is not logged.
type target struct {
	protocol    provider.Protocol
	endpoint    string
	countTokens bool
}

// targetFor resolves the target of an executor format.
func (s *Service) targetFor(format string) (target, *pluginabi.Error) {
	protocol, ok := provider.ProtocolForFormat(format)
	if !ok {
		return target{}, upstream.RequestError("unsupported_format", s.errors.Prefix+"unsupported executor format "+format)
	}
	return target{protocol: protocol, endpoint: s.adapter.Endpoint(protocol)}, nil
}

// upstreamCall is a fully prepared upstream request.
type upstreamCall struct {
	endpoint string
	url      string
	body     []byte
	header   http.Header
	client   *http.Client
	authID   string
	// model, upstreamModel, effort, keyMasked and egress describe the call
	// for the request log.
	model         string
	upstreamModel string
	effort        string
	keyMasked     string
	egress        string
	// fix repairs the call's stream (provider.StreamFixer); nil when the
	// adapter leaves it as it is.
	fix provider.StreamFix
}

// prepareCall resolves the credential, egress and upstream model for one
// executor request.
func (s *Service) prepareCall(req pluginapi.ExecutorRequest, to target) (*upstreamCall, *pluginabi.Error) {
	s.noteAuth(req.AuthID)
	cred, _, errCred := credential.FromMaps(req.StorageJSON, req.AuthMetadata)
	if errCred != nil {
		return nil, upstream.AuthError(s.errors.Prefix + errCred.Error())
	}
	if cred.APIKey == "" {
		return nil, upstream.AuthError(s.errors.Prefix + "credential has no api_key")
	}
	cfg := s.config()
	members, errScan := payload.Members(req.Payload, s.requestMembers...)
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = payload.String(req.Payload, members, "model")
	}
	if model == "" {
		return nil, upstream.RequestError("invalid_request", s.errors.Prefix+"request has no model")
	}
	upstreamModel := s.catalog.Current().UpstreamID(model, cred.EffectivePrefix(cfg))
	encodedUpstream, _ := json.Marshal(upstreamModel)
	body, errBody := payload.Set(req.Payload, members, errScan, "model", encodedUpstream)
	if errBody != nil {
		return nil, upstream.RequestError("invalid_request", s.errors.Prefix+errBody.Error())
	}
	if _, present := members["stream_options"]; to.protocol == provider.Chat && req.Stream && !present && errScan == nil {
		// Ask for the final usage chunk so CPA can account the request.
		body = payload.Insert(body, "stream_options", []byte(`{"include_usage":true}`))
	}
	effort := ""
	if errScan == nil {
		effort = reasoningEffort(req.Payload, members)
	}
	setting, errProxy := egress.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, &pluginabi.Error{Code: "invalid_proxy", Message: s.errors.Prefix + errProxy.Error(), HTTPStatus: http.StatusBadGateway}
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cred.APIKey)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", s.userAgent())
	if req.Stream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	for _, name := range s.forwardHeaders {
		if value := strings.TrimSpace(req.Headers.Get(name)); value != "" {
			header.Set(name, value)
		}
	}
	if s.decorator != nil {
		s.decorator.Decorate(header, provider.CallInput{Request: req, Members: members, ScanErr: errScan})
	}
	if to.protocol == provider.Messages {
		// The providers reject versions older than 2023-06-01.
		if header.Get("Anthropic-Version") == "" {
			header.Set("Anthropic-Version", defaultAnthropicVersion)
		}
	} else {
		header.Del("Anthropic-Beta")
		header.Del("Anthropic-Version")
	}
	var fix provider.StreamFix
	if s.fixer != nil && !to.countTokens {
		fix = s.fixer.StreamFix(to.protocol, provider.CallInput{Request: req, Members: members, ScanErr: errScan})
	}
	return &upstreamCall{
		endpoint: to.endpoint,
		url:      cred.EffectiveBaseURL(cfg) + to.endpoint,
		body:     body,
		header:   header,
		client:   s.clients.Client(setting),
		authID:   req.AuthID,

		model:         clientModel(req, model),
		upstreamModel: upstreamModel,
		effort:        effort,
		keyMasked:     credential.MaskKey(cred.APIKey),
		egress:        setting.Label(),
		fix:           fix,
	}, nil
}

func (c *upstreamCall) newRequest(ctx context.Context) (*http.Request, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(c.body))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header = c.header.Clone()
	return httpReq, nil
}

// execute performs a buffered request and returns the upstream body as is.
func (s *Service) execute(req pluginapi.ExecutorRequest) []byte {
	to, perr := s.targetFor(req.Format)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	return s.executeBuffered(req, to)
}

// countTokens forwards an Anthropic count_tokens request to the adapter's
// provider.TokenCounter (Call refuses it without one). It is not written to
// the request log: it generates nothing and costs nothing.
func (s *Service) countTokens(req pluginapi.ExecutorRequest) []byte {
	return s.executeBuffered(req, target{protocol: provider.Messages, endpoint: s.counter.CountTokensEndpoint(), countTokens: true})
}

func (s *Service) executeBuffered(req pluginapi.ExecutorRequest, to target) []byte {
	call, perr := s.prepareCall(req, to)
	if perr != nil {
		if !to.countTokens {
			s.recordFailure(req, to.endpoint, false, perr)
		}
		return errorEnvelopeFrom(perr)
	}
	var rec *callRecord
	if !to.countTokens {
		rec = s.startRecord(req, call, false)
	}
	fail := func(perr *pluginabi.Error) []byte {
		rec.finish(perr, "request")
		return errorEnvelopeFrom(perr)
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config().RequestTimeout)
	defer cancel()
	httpReq, errReq := call.newRequest(ctx)
	if errReq != nil {
		return fail(s.errors.Transport(errReq))
	}
	resp, errDo := call.client.Do(httpReq)
	if errDo != nil {
		return fail(s.errors.Transport(errDo))
	}
	defer func() { _ = resp.Body.Close() }()
	rec.response(resp)
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if errRead != nil {
		return fail(s.errors.Transport(fmt.Errorf("read response: %w", errRead)))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(s.errors.Upstream(resp.StatusCode, resp.Header, body))
	}
	rec.observeJSON(body)
	rec.finish(nil, "")
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: body,
		Headers: filterHeaders(resp.Header, forwardedResponseHeaders),
	})
}

func filterHeaders(source http.Header, names []string) http.Header {
	out := http.Header{}
	for _, name := range names {
		if values := source.Values(name); len(values) > 0 {
			out[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return out
}
