package modelcontext

import "strings"

// EvidenceCitation identifies a server-registered observation or calculation,
// not a fabricated document chunk. Only the current execution path calls
// RegisterEvidenceCitation; reading a saved tag grants navigation, never proof.
type EvidenceCitation struct {
	ID    string
	Title string
	Kind  string
}

func (r *sourceRegistry) registerEvidence(ref EvidenceCitation, current bool) string {
	if r == nil || strings.TrimSpace(ref.ID) == "" || len(ref.ID) > 128 || strings.ContainsAny(ref.ID, "<>\"'/\\\r\n\t ") || isSourceHandleShaped(ref.ID) {
		return ""
	}
	handle := r.evidence.register(ref.ID, ref.ID, ref, nil)
	if current {
		r.citable.Store(handle, true)
	}
	return handle
}

func (r *Registry) RegisterEvidenceCitation(ref EvidenceCitation) string {
	if r == nil || r.sources == nil {
		return ""
	}
	return r.sources.registerEvidence(ref, true)
}

func (r *Registry) CitableEvidenceHandle(id string) string {
	if r == nil || r.sources == nil {
		return ""
	}
	handle, ok := r.sources.evidence.handleForKey(id)
	if !ok {
		return ""
	}
	if _, ok := r.sources.citable.Load(handle); !ok {
		return ""
	}
	return handle
}
