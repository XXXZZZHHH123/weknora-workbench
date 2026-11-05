package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnverifiedEvidencePublicStatusIsOnlyPresentationAndRoundTripsInStorage(t *testing.T) {
	ref := EvidenceReference{ID: "ev_check", Kind: EvidenceKindRawCell, ResultID: "synthetic-result", ResultHash: strings.Repeat("a", 64), CandidateHash: strings.Repeat("b", 64), ReviewStatus: "unverified",
		Sources: []EvidenceSourceSnapshot{{KnowledgeBaseID: "kb", KnowledgeID: "doc", SourceSHA256: strings.Repeat("c", 64)}}, Manifest: json.RawMessage(`{"result":{"columns":["quantity"],"rows":[[27]]}}`)}
	require.NoError(t, ref.ValidateStored())
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"review_status":"unverified"`)
	var summary EvidenceReference
	require.NoError(t, json.Unmarshal(raw, &summary))
	require.Error(t, summary.ValidateStored(), "a presentation status cannot create missing current source evidence")
	raw, err = json.Marshal(ref.StorageEnvelope())
	require.NoError(t, err)
	var restored StoredEvidenceEnvelope
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, ref, restored.FullReference())
	for _, status := range []string{"accepted", "reviewed", "complete", "verified"} {
		ref.ReviewStatus = status
		require.Error(t, ref.ValidateStored(), "no public status grants semantic acceptance")
	}
}
