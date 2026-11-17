package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/provider"
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
	// extension is what the adapter's provider.CredentialExtension decoded
	// from the same body.
	extension any
}

type credentialResult struct {
	OK            bool            `json:"ok"`
	Credential    *credentialView `json:"credential,omitempty"`
	UnknownModels []string        `json:"unknown_models,omitempty"`
}

func (s *Service) decodeCredentialInput(body []byte) (credentialInput, *errorBody) {
	var input credentialInput
	if len(bytes.TrimSpace(body)) == 0 {
		return input, &errorBody{Error: "invalid_json", Message: "request body is required"}
	}
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return input, &errorBody{Error: "invalid_json", Message: errUnmarshal.Error()}
	}
	if s.credExt != nil {
		extension, errDecode := s.credExt.DecodeCredentialInput(body)
		if errDecode != nil {
			return input, &errorBody{Error: "invalid_json", Message: errDecode.Error()}
		}
		input.extension = extension
	}
	return input, nil
}

// applyCredentialInput validates input and writes it into an auth file's
// fields; cfg normalises base URLs and ext, when not nil, applies the
// provider's own members.
func applyCredentialInput(fields map[string]any, input credentialInput, cfg config.Config, ext provider.CredentialExtension) *errorBody {
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
		saved, _ := fields[credential.KeyProxyURL].(string)
		proxy, errHidden := keepHiddenPassword(strings.TrimSpace(*input.ProxyURL), saved)
		if errHidden != nil {
			return errHidden
		}
		if errProxy := egress.ValidateProxy(proxy); errProxy != nil {
			return &errorBody{Error: "invalid_proxy", Message: "proxy_url: " + errProxy.Error()}
		}
		setOrDelete(fields, credential.KeyProxyURL, proxy)
	}
	if ext != nil {
		if errExt := ext.ApplyCredentialInput(fields, input.extension); errExt != nil {
			return &errorBody{Error: errExt.Code, Message: errExt.Message}
		}
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

// redactedPassword is what the views show in place of a proxy password
// (url.URL.Redacted).
const redactedPassword = "xxxxx"

// keepHiddenPassword puts the saved password back into a proxy address
// edited from the redacted one the views show. The edit form shows a saved
// custom address with its password as xxxxx, so changing another part of
// it, such as the port, sends xxxxx back. The saved password is kept for the
// same scheme, user and host; for another one it has to be typed again,
// rather than saving xxxxx or sending the password somewhere new. An
// xxxxx password with no saved one to restore is taken as typed.
func keepHiddenPassword(proxy, saved string) (string, *errorBody) {
	typed, errTyped := url.Parse(proxy)
	if errTyped != nil || typed.User == nil {
		return proxy, nil
	}
	if password, set := typed.User.Password(); !set || password != redactedPassword {
		return proxy, nil
	}
	old, errOld := url.Parse(strings.TrimSpace(saved))
	if errOld != nil || old.User == nil {
		return proxy, nil
	}
	oldPassword, oldSet := old.User.Password()
	if !oldSet {
		return proxy, nil
	}
	if !strings.EqualFold(typed.Scheme, old.Scheme) || typed.User.Username() != old.User.Username() || !strings.EqualFold(typed.Hostname(), old.Hostname()) {
		return "", &errorBody{Error: "proxy_password_hidden", Message: "proxy_url: the saved password is hidden as " + redactedPassword + " and is kept only for the same scheme, user and host; type the password again"}
	}
	typed.User = url.UserPassword(old.User.Username(), oldPassword)
	return typed.String(), nil
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
	input, errInput := s.decodeCredentialInput(body)
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
	if errApply := applyCredentialInput(fields, input, s.config(), s.credExt); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	cred := credential.FromMap(fields)
	if !input.SkipVerify {
		if perr := s.verifyKey(cred); perr != nil {
			return jsonResponse(verifyStatus(perr), errorBody{Error: "key_verification_failed", Message: perr.Message})
		}
		if s.credExt != nil {
			if check := s.credExt.CheckCredential(cred, nil); check != nil {
				if resp, failed := s.checkCredential(cred, check); failed {
					return resp
				}
			}
		}
	}
	// Load the catalog before saving so the file records the revision its
	// models are registered from and the background sync leaves it alone.
	snapshot := s.catalogFor(cred)
	if revision := revisionFor(snapshot, cred); revision != "" {
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
	input, errInput := s.decodeCredentialInput(body)
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
	if errApply := applyCredentialInput(fields, input, s.config(), s.credExt); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	if s.credExt != nil && !input.SkipVerify {
		updated, previous := credential.FromMap(fields), entry.cred
		if check := s.credExt.CheckCredential(updated, &previous); check != nil {
			if resp, failed := s.checkCredential(updated, check); failed {
				return resp
			}
		}
	}
	// As when adding: load the catalog before saving so the file records
	// the revision its models are registered from and the background sync
	// does not save it again.
	snapshot := s.catalogFor(credential.FromMap(fields))
	if revision := revisionFor(snapshot, credential.FromMap(fields)); revision != "" {
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
	view := s.credentialViewOf(entry, s.config(), s.catalog.Current(), s.registeredModels(), proxies)
	return &view
}

func (s *Service) credentialQuota(authIndex string) pluginapi.ManagementResponse {
	cred, ok := s.credentialByAuthIndex(authIndex)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no " + s.spec.DisplayName + " credential matches auth_index"})
	}
	info, perr := s.fetchAccount(cred, true)
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
