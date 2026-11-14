package core

import (
	"net/http"
	"time"

	"github.com/berry-shake/cpa-provider-core/egress"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/requestlog"
)

// serviceHost is the provider.Host a provider.HostUser adapter gets.
type serviceHost struct {
	s *Service
}

var _ provider.Host = serviceHost{}

func (h serviceHost) Env() provider.Env {
	return h.s.env()
}

func (h serviceHost) Logf(level, message string, fields map[string]any) {
	h.s.logf(level, message, fields)
}

func (h serviceHost) AuthFiles() ([]provider.AuthFile, error) {
	entries, errList := h.s.listAuths()
	if errList != nil {
		return nil, errList
	}
	out := make([]provider.AuthFile, 0, len(entries))
	for _, entry := range entries {
		out = append(out, provider.AuthFile{
			HostAuthFileEntry: entry.HostAuthFileEntry,
			Fields:            entry.fields,
			Credential:        entry.cred,
			ReadError:         entry.readError,
		})
	}
	return out, nil
}

func (h serviceHost) ReadAuthFile(authIndex string) (map[string]any, string, error) {
	return h.s.authFile(authIndex)
}

func (h serviceHost) SaveAuthFile(name string, fields map[string]any) error {
	return h.s.saveAuthFile(name, fields)
}

func (h serviceHost) Client(proxyURL string) (*http.Client, error) {
	setting, errProxy := egress.ResolveEgress(proxyURL, h.s.globalProxy())
	if errProxy != nil {
		return nil, errProxy
	}
	return h.s.clients.Client(setting), nil
}

func (h serviceHost) Usage(authIndex, authID string, now time.Time) (requestlog.Usage, string) {
	return h.s.reqlog.Usage(authIndex, authID, now)
}
