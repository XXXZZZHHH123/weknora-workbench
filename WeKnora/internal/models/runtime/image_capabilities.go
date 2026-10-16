package runtime

import (
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/providers"
	"github.com/Tencent/WeKnora/internal/types"
)

// ChatAcceptsImages also recognizes older OpenAI-compatible rows saved as
// "generic" against a vendor's exact declared chat endpoint. This supplements
// input metadata only: the configured provider, wire protocol and credentials
// are never changed. Explicit per-row input restrictions remain authoritative.
func ChatAcceptsImages(ref Ref) bool { return Default().ChatAcceptsImages(ref) }

func (rt *Runtime) ChatAcceptsImages(ref Ref) bool {
	if ref.ModelType != "" && ref.ModelType != types.ModelTypeKnowledgeQA && ref.ModelType != types.ModelTypeVLLM {
		return false
	}
	resolved, err := rt.Resolve(ref)
	if err != nil {
		return false
	}
	if resolved.Spec.AcceptsImages() {
		return true
	}
	if ref.Override != nil && len(ref.Override.Input) > 0 {
		return false
	}
	if strings.ToLower(strings.TrimSpace(ref.Provider)) != providers.GenericID {
		return false
	}
	endpoint, ok := catalogCapabilityEndpoint(ref.BaseURL)
	if !ok {
		return false
	}
	for _, vendor := range rt.List() {
		if vendor.ID == providers.GenericID {
			continue
		}
		known, ok := catalogCapabilityEndpoint(vendor.GetDefaultURL(types.ModelTypeKnowledgeQA))
		if !ok || known != endpoint {
			continue
		}
		candidate, err := vendor.Resolve(ref)
		if err == nil && candidate.Cataloged && candidate.API == resolved.API && candidate.Spec.AcceptsImages() {
			return true
		}
	}
	return false
}

func catalogCapabilityEndpoint(value string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return strings.ToLower(u.Hostname()) + ":" + port + strings.TrimRight(u.Path, "/"), true
}
