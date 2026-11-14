package core

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// maxManagementBody bounds request bodies of the management routes.
const maxManagementBody = 1 << 20

// paths are the management routes and resources of a plugin.
type paths struct {
	logo, page, flag                                        string
	management                                              string
	status, models, modelsRefresh, credentials, quota, logs string
	proxies, proxiesCheck, proxiesImport, proxiesOrder      string
}

func pathsFor(pluginID string) paths {
	management := "/v0/management/plugins/" + pluginID
	resource := "/v0/resource/plugins/" + pluginID
	return paths{
		logo: resource + "/logo.svg",
		page: resource + "/manage",
		flag: resource + "/flag.svg",

		management: management,

		status:        management + "/status",
		models:        management + "/models",
		modelsRefresh: management + "/models/refresh",
		credentials:   management + "/credentials",
		quota:         management + "/credentials/quota",
		logs:          management + "/logs",

		proxies:       management + "/proxies",
		proxiesCheck:  management + "/proxies/check",
		proxiesImport: management + "/proxies/import",
		proxiesOrder:  management + "/proxies/order",
	}
}

// routeDeclaration is a /v0/management route; CPA checks the management key
// before forwarding the request to the plugin.
type routeDeclaration struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// resourceDeclaration is a /v0/resource route; CPA does not authenticate it.
type resourceDeclaration struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistration struct {
	Routes    []routeDeclaration    `json:"routes,omitempty"`
	Resources []resourceDeclaration `json:"resources,omitempty"`
}

func (s *Service) managementRoutes() managementRegistration {
	p, texts, name := s.paths, s.spec.Texts, s.spec.DisplayName
	reg := managementRegistration{
		Routes: []routeDeclaration{
			{Method: http.MethodGet, Path: p.status, Description: "Plugin status, model catalog summary and " + name + " credentials."},
			{Method: http.MethodGet, Path: p.models, Description: texts.ModelsRoute},
			{Method: http.MethodPost, Path: p.modelsRefresh, Description: texts.ModelsRefreshRoute},
			{Method: http.MethodPost, Path: p.credentials, Description: texts.AddCredentialRoute},
			{Method: http.MethodPatch, Path: p.credentials, Description: texts.EditCredentialRoute},
			{Method: http.MethodGet, Path: p.quota, Description: texts.QuotaRoute},
		},
		Resources: []resourceDeclaration{
			{Path: "/manage", Menu: name, Description: "Manage " + name + " API keys, model selection and egress proxies."},
			{Path: "/logo.svg", Description: "Plugin logo served to the management panel sidebar."},
			{Path: "/flag.svg", Description: "Country flag icon for egress checks (?cc=<ISO 3166-1 alpha-2>)."},
		},
	}
	for _, route := range s.routes {
		reg.Routes = append(reg.Routes, routeDeclaration{Method: route.Method, Path: p.management + route.Path, Description: route.Description})
	}
	reg.Routes = append(reg.Routes, []routeDeclaration{
		{Method: http.MethodGet, Path: p.logs, Description: "Request log, newest first (?search=&status=all|success|error&from=&to=&request_id=&limit=&offset=), with a summary."},
		{Method: http.MethodDelete, Path: p.logs, Description: "Clear the request log."},
		{Method: http.MethodGet, Path: p.proxies, Description: "Proxy list (URLs redacted) and CPA's global proxy."},
		{Method: http.MethodPost, Path: p.proxies, Description: `Add a proxy. Body: {"name","url"}.`},
		{Method: http.MethodPatch, Path: p.proxies, Description: `Rename a proxy or change its URL. Body: {"id","name","url"}. Credentials using the old URL move to the new one.`},
		{Method: http.MethodDelete, Path: p.proxies, Description: "Remove a proxy (?id=). Credentials using it keep its URL."},
		{Method: http.MethodPost, Path: p.proxiesImport, Description: "Add the proxies " + name + " credentials already use to the list."},
		{Method: http.MethodPost, Path: p.proxiesOrder, Description: `Reorder the proxy list. Body: {"ids":[...]}.`},
		{Method: http.MethodPost, Path: p.proxiesCheck, Description: `Look up the exit IP and latency of a proxy. Body: {"id"} or {"target":"direct"|"inherit"}.`},
	}...)
	return reg
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// handleManagement serves the authenticated /v0/management routes and the
// unauthenticated page and logo resources.
func (s *Service) handleManagement(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if len(req.Body) > maxManagementBody {
		return jsonResponse(http.StatusRequestEntityTooLarge, errorBody{Error: "body_too_large", Message: "request body is too large"})
	}
	p := s.paths
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	switch path {
	case p.logo:
		if method == http.MethodGet {
			return s.logoResponse()
		}
	case p.page:
		if method == http.MethodGet {
			return s.pageResponse()
		}
	case p.flag:
		if method == http.MethodGet {
			return flagResponse(req.Query.Get("cc"))
		}
	case p.status:
		if method == http.MethodGet {
			return jsonResponse(http.StatusOK, s.status())
		}
	case p.models:
		if method == http.MethodGet {
			return s.models()
		}
	case p.modelsRefresh:
		if method == http.MethodPost {
			return s.refreshModels()
		}
	case p.credentials:
		switch method {
		case http.MethodPost:
			return s.addCredential(req.Body)
		case http.MethodPatch:
			return s.updateCredential(req.Body)
		}
	case p.quota:
		if method == http.MethodGet {
			if s.quotaPage != nil {
				return s.quotaPage.ServeQuota(req)
			}
			return s.credentialQuota(req.Query.Get("auth_index"))
		}
	case p.logs:
		switch method {
		case http.MethodGet:
			limit, _ := strconv.Atoi(req.Query.Get("limit"))
			offset, _ := strconv.Atoi(req.Query.Get("offset"))
			from, errFrom := parseLogTime(req.Query.Get("from"))
			to, errTo := parseLogTime(req.Query.Get("to"))
			if errFrom != nil || errTo != nil {
				return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_range", Message: "from and to must be RFC 3339 times"})
			}
			return jsonResponse(http.StatusOK, s.reqlog.Query(requestlog.Query{
				Search:    req.Query.Get("search"),
				Status:    req.Query.Get("status"),
				RequestID: req.Query.Get("request_id"),
				From:      from,
				To:        to,
				Limit:     limit,
				Offset:    offset,
			}))
		case http.MethodDelete:
			if errClear := s.reqlog.Clear(); errClear != nil {
				return jsonResponse(http.StatusInternalServerError, errorBody{Error: "clear_failed", Message: errClear.Error()})
			}
			return jsonResponse(http.StatusOK, map[string]bool{"ok": true})
		}
	case p.proxies:
		switch method {
		case http.MethodGet:
			return jsonResponse(http.StatusOK, s.proxiesView())
		case http.MethodPost:
			return s.addProxy(req.Body)
		case http.MethodPatch:
			return s.updateProxy(req.Body)
		case http.MethodDelete:
			return s.deleteProxy(req.Query.Get("id"))
		}
	case p.proxiesImport:
		if method == http.MethodPost {
			return s.importProxies()
		}
	case p.proxiesOrder:
		if method == http.MethodPost {
			return s.reorderProxies(req.Body)
		}
	case p.proxiesCheck:
		if method == http.MethodPost {
			return s.checkProxy(req.Body)
		}
	default:
		if resp, known := s.handleExtension(path, method, req); known {
			return resp
		}
		return jsonResponse(http.StatusNotFound, errorBody{Error: "not_found", Message: "plugin route not found"})
	}
	return jsonResponse(http.StatusMethodNotAllowed, errorBody{Error: "method_not_allowed", Message: "method not allowed on this route"})
}

// handleExtension serves the adapter's own routes (provider.ManagementExtension);
// known is false when none has the path.
func (s *Service) handleExtension(path, method string, req pluginapi.ManagementRequest) (resp pluginapi.ManagementResponse, known bool) {
	for _, route := range s.routes {
		if s.paths.management+route.Path != path {
			continue
		}
		known = true
		if route.Method == method {
			return route.Handle(req), true
		}
	}
	if known {
		return jsonResponse(http.StatusMethodNotAllowed, errorBody{Error: "method_not_allowed", Message: "method not allowed on this route"}), true
	}
	return pluginapi.ManagementResponse{}, false
}

func jsonResponse(status int, v any) pluginapi.ManagementResponse {
	return provider.JSONResponse(status, v)
}

// parseLogTime reads a request log range bound; empty means unbounded.
func parseLogTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}
