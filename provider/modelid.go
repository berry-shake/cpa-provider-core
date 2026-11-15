package provider

import "strings"

// IDShape tells UpstreamID whether the upstream's model IDs may contain a
// slash, which decides what happens to a prefix it does not know.
type IDShape int

const (
	// SlashedIDs are upstream IDs that may contain a slash ("vendor/model"):
	// an unknown prefix is dropped only when what follows it is a catalog
	// model, so a slashed ID the catalog does not list yet reaches the
	// upstream whole.
	SlashedIDs IDShape = iota
	// FlatIDs are upstream IDs that never contain a slash (Wokey): an unknown
	// prefix is always dropped rather than sent upstream.
	FlatIDs
)

// UpstreamID maps the model CPA hands the executor back to the upstream's
// model ID; adapters implement Catalog.UpstreamID with it. CPA strips the
// auth prefix and the reasoning suffix before calling the executor; both are
// still removed here in case a model arrives with them:
//
//   - a trailing reasoning suffix is dropped (StripThinkingSuffix);
//   - the credential's prefix ("<prefix>/") is dropped;
//   - a model the catalog has passes as it is;
//   - a prefix we do not know (for example from a changed config) is dropped
//     as shape says.
//
// has reports whether the catalog holds an upstream ID.
func UpstreamID(model, prefix string, has func(id string) bool, shape IDShape) string {
	model = StripThinkingSuffix(strings.TrimSpace(model))
	if prefix != "" && strings.HasPrefix(model, prefix+"/") {
		return strings.TrimPrefix(model, prefix+"/")
	}
	if has(model) {
		return model
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		if rest := model[idx+1:]; shape == FlatIDs || has(rest) {
			return rest
		}
	}
	return model
}

// StripThinkingSuffix removes a trailing CPA reasoning suffix such as
// "(high)" or "(8192)". CPA normally strips it before calling the executor.
func StripThinkingSuffix(model string) string {
	if !strings.HasSuffix(model, ")") {
		return model
	}
	open := strings.LastIndex(model, "(")
	if open <= 0 {
		return model
	}
	return strings.TrimSpace(model[:open])
}
