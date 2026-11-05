package session

import "github.com/Tencent/WeKnora/internal/types"

func publicEventEvidenceReferences(value interface{}) types.EvidenceReferences {
	switch refs := value.(type) {
	case types.EvidenceReferences:
		return refs.PublicSummaries()
	case []types.EvidenceReference:
		return types.EvidenceReferences(refs).PublicSummaries()
	case []interface{}:
		return types.EvidenceReferencesFromInternal(refs).PublicSummaries()
	default:
		return nil
	}
}
