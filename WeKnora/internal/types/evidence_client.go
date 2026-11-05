package types

import "context"

const EvidenceReferencesClientHeader = "X-WeKnora-Evidence-Version"
const evidenceReferencesClientContextKey ContextKey = "EvidenceReferencesClientVersion"

// This is a display-protocol negotiation, never an authorization grant.
// Missing/unknown versions keep older clients on their existing citations.
func WithEvidenceReferencesClientVersion(ctx context.Context, version string) context.Context {
	return context.WithValue(ctx, evidenceReferencesClientContextKey, version == "1")
}

func EvidenceReferencesClientEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(evidenceReferencesClientContextKey).(bool)
	return enabled
}
