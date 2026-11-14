package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Adding and editing credentials from the management page.

// credentialInput is the editable part of an auth file. Pointer and
// RawMessage fields distinguish "absent" from "clear".
type credentialInput struct {
	AuthIndex  string  `json:"auth_index"`
	Name       string  `json:"name"`
	APIKey     string  `json:"api_key"`
	SkipVerify bool    `json:"skip_verify"`
	Label      *string `json:"label"`
	Note       *string `json:"note"`
	BaseURL    *string `json:"base_url"`
	ProxyURL   *string `json:"proxy_url"`
	// ProxyID picks a listed proxy; it overrides ProxyURL.
	ProxyID  *string         `json:"proxy_id"`
	Prefix   json.RawMessage `json:"prefix"`
	Models   json.RawMessage `json:"models"`
	Priority json.RawMessage `json:"priority"`
}

type credentialResult struct {
	OK            bool            `json:"ok"`
	Credential    *credentialView `json:"credential,omitempty"`
	UnknownModels []string        `json:"unknown_models,omitempty"`
}

func decodeCredentialInput(body []byte) (credentialInput, *errorBody) {
	var input credentialInput
	if len(bytes.TrimSpace(body)) == 0 {
		return input, &errorBody{Error: "invalid_json", Message: "request body is required"}
	}
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return input, &errorBody{Error: "invalid_json", Message: errUnmarshal.Error()}
	}
	return input, nil
}

// applyCredentialInput validates input and writes it into an auth file's
// fields; cfg normalises base URLs.
func applyCredentialInput(fields map[string]any, input credentialInput, cfg config.Config) *errorBody {
	if input.Label != nil {
		setOrDelete(fields, credential.KeyLabel, strings.TrimSpace(*input.Label))
	}
	if input.Note != nil {
		setOrDelete(fields, credential.KeyNote, strings.TrimSpace(*input.Note))
	}
	if input.BaseURL != nil {
		base := strings.TrimSpace(*input.BaseURL)
		if base != "" {
			normalized, errBase := cfg.NormalizeBaseURL(base)
			if errBase != nil {
				return &errorBody{Error: "invalid_base_url", Message: "base_url: " + errBase.Error()}
			}
			base = normalized
		}
		setOrDelete(fields, credential.KeyBaseURL, base)
	}
	if input.ProxyURL != nil {
		proxy := strings.TrimSpace(*input.ProxyURL)
		if errProxy := egress.ValidateProxy(proxy); errProxy != nil {
			return &errorBody{Error: "invalid_proxy", Message: "proxy_url: " + errProxy.Error()}
		}
		setOrDelete(fields, credential.KeyProxyURL, proxy)
	}
	if len(input.Prefix) > 0 {
		if isJSONNull(input.Prefix) {
			delete(fields, credential.KeyPrefix)
		} else {
			var prefix string
			if errPrefix := json.Unmarshal(input.Prefix, &prefix); errPrefix != nil {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must be a string or null"}
			}
			prefix = config.NormalizePrefix(prefix)
			if strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must not contain spaces"}
			}
			fields[credential.KeyPrefix] = prefix
		}
	}
	if len(input.Models) > 0 {
		if isJSONNull(input.Models) {
			delete(fields, credential.KeyModels)
		} else {
			var models []string
			if errModels := json.Unmarshal(input.Models, &models); errModels != nil {
				return &errorBody{Error: "invalid_models", Message: "models must be an array of model IDs or null for all models"}
			}
			models = credential.NormalizeModels(models)
			for _, id := range models {
				if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
					return &errorBody{Error: "invalid_models", Message: "model IDs must not contain spaces"}
				}
			}
			fields[credential.KeyModels] = models
		}
	}
	if len(input.Priority) > 0 {
		if isJSONNull(input.Priority) {
			delete(fields, credential.KeyPriority)
		} else {
			var priority int
			if errPriority := json.Unmarshal(input.Priority, &priority); errPriority != nil {
				return &errorBody{Error: "invalid_priority", Message: "priority must be an integer or null"}
			}
			if priority == 0 {
				delete(fields, credential.KeyPriority)
			} else {
				fields[credential.KeyPriority] = priority
			}
		}
	}
	return nil
}

func setOrDelete(fields map[string]any, key, value string) {
	if value == "" {
		delete(fields, key)
		return
	}
	fields[key] = value
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// addCredential validates a new API key with the adapter (Adapter.VerifyKey)
// and saves it as an auth file named after the key's fingerprint.
func (s *Service) addCredential(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeCredentialInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	if errProxy := s.resolveProxyID(&input); errProxy != nil {
		return jsonResponse(http.StatusBadRequest, *errProxy)
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_api_key", Message: "api_key is required and must not contain spaces"})
	}
	entries, errList := s.listAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	name := credential.FileNameForKey(s.spec.ID, key)
	for _, entry := range entries {
		if entry.cred.APIKey == key || strings.EqualFold(credential.SaveName(entry.Name, entry.Path), name) {
			return jsonResponse(http.StatusConflict, errorBody{Error: "duplicate_key", Message: "this API key is already added as " + entry.Name})
		}
	}

	fields := map[string]any{credential.KeyType: s.spec.ID, credential.KeyAPIKey: key}
	if errApply := applyCredentialInput(fields, input, s.config()); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	cred := credential.FromMap(fields)
	if !input.SkipVerify {
		if perr := s.verifyKey(cred); perr != nil {
			return jsonResponse(verifyStatus(perr), errorBody{Error: "key_verification_failed", Message: perr.Message})
		}
	}
	// Load the catalog before saving so the file records the revision its
	// models are registered from and the background sync leaves it alone.
	snapshot := s.catalog.Current()
	if empty(snapshot) {
		snapshot = s.loadCatalogFor(cred)
	}
	if revision := snapshot.Summary().Revision; revision != "" {
		fields[credential.KeyModelRevision] = revision
	}
	if errSave := s.saveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	s.logf("info", s.errors.Prefix+"added credential", map[string]any{"auth": name})
	return jsonResponse(http.StatusCreated, credentialResult{OK: true, Credential: s.credentialViewByName(name), UnknownModels: missingModels(cred, snapshot)})
}

// updateCredential edits label, models, prefix, proxy, base URL, note or
// priority of an existing auth file. The API key itself is immutable;
// add a new credential instead.
func (s *Service) updateCredential(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeCredentialInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	if strings.TrimSpace(input.APIKey) != "" {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "immutable_api_key", Message: "the API key cannot be changed; add a new credential instead"})
	}
	if errProxy := s.resolveProxyID(&input); errProxy != nil {
		return jsonResponse(http.StatusBadRequest, *errProxy)
	}
	entries, errList := s.listAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	entry, found := findEntry(entries, input.AuthIndex, input.Name)
	if !found {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no " + s.spec.DisplayName + " credential matches auth_index/name"})
	}
	if entry.readError != "" {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "read_failed", Message: entry.readError})
	}
	name := credential.SaveName(entry.Name, entry.Path)
	if name == "" {
		return jsonResponse(http.StatusConflict, errorBody{Error: "not_file_backed", Message: "this credential has no auth file the plugin can edit"})
	}
	fields := cloneFields(entry.fields)
	if errApply := applyCredentialInput(fields, input, s.config()); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	snapshot := s.catalog.Current()
	if revision := snapshot.Summary().Revision; revision != "" {
		fields[credential.KeyModelRevision] = revision
	}
	if errSave := s.saveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	s.logf("info", s.errors.Prefix+"updated credential", map[string]any{"auth": name})
	return jsonResponse(http.StatusOK, credentialResult{OK: true, Credential: s.credentialViewByName(name), UnknownModels: missingModels(credential.FromMap(fields), snapshot)})
}

// verifyStatus reports a rejected key (or a suspended account) as 400
// and anything else (unreachable, rate limited, 5xx) as 502.
func verifyStatus(perr *pluginabi.Error) int {
	switch perr.HTTPStatus {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func findEntry(entries []authEntry, authIndex, name string) (authEntry, bool) {
	authIndex = strings.TrimSpace(authIndex)
	name = strings.TrimSpace(name)
	for _, entry := range entries {
		if authIndex != "" && entry.AuthIndex == authIndex {
			return entry, true
		}
		if name != "" && (entry.Name == name || credential.SaveName(entry.Name, entry.Path) == name) {
			return entry, true
		}
	}
	return authEntry{}, false
}

// credentialViewByName re-reads one credential after a save, so the response
// shows what CPA actually registered.
func (s *Service) credentialViewByName(name string) *credentialView {
	entries, errList := s.listAuths()
	if errList != nil {
		return nil
	}
	entry, found := findEntry(entries, "", name)
	if !found {
		return nil
	}
	proxies, _ := s.proxies.List()
	view := credentialViewOf(entry, s.config(), s.catalog.Current(), s.registeredModels(), proxies)
	return &view
}

func (s *Service) credentialQuota(authIndex string) pluginapi.ManagementResponse {
	cred, ok := s.credentialByAuthIndex(authIndex)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no " + s.spec.DisplayName + " credential matches auth_index"})
	}
	info, perr := s.fetchAccount(cred)
	if perr != nil {
		// Upstream 401/403 describe the provider key, not the management key;
		// keep them out of the page's "management key rejected" handling.
		status := http.StatusBadGateway
		if perr.HTTPStatus >= 400 && perr.HTTPStatus < 500 {
			status = http.StatusBadRequest
		}
		return jsonResponse(status, errorBody{Error: perr.Code, Message: perr.Message})
	}
	return jsonResponse(http.StatusOK, info)
}
