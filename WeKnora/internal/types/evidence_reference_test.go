package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceReferencePublicSerializationCannotReplayPrivateProof(t *testing.T) {
	ref := EvidenceReference{ID: "ev_test", Kind: EvidenceKindRawCell, ResultID: "result", ResultHash: strings.Repeat("a", 64), CandidateHash: strings.Repeat("b", 64), Sources: []EvidenceSourceSnapshot{{KnowledgeBaseID: "library", KnowledgeID: "original", SourceSHA256: strings.Repeat("c", 64)}}, Manifest: json.RawMessage(`{"private_source":"PRIVATE_SOURCE_VALUE"}`), ClaimBindings: json.RawMessage(`[{"internal_binding":true}]`)}
	require.NoError(t, ref.ValidateStored())
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	for _, secret := range []string{"PRIVATE_SOURCE_VALUE", "private_source", "internal_binding", "source_sha256", "sources", "manifest", "claim_bindings"} {
		require.NotContains(t, string(raw), secret)
	}
	var public EvidenceReference
	require.NoError(t, json.Unmarshal(raw, &public))
	require.Error(t, public.ValidateStored(), "a public reference is not a current executable/citable observation")
	roundTrip, err := json.Marshal(ref.StorageEnvelope())
	require.NoError(t, err)
	var stored StoredEvidenceEnvelope
	require.NoError(t, json.Unmarshal(roundTrip, &stored))
	require.Equal(t, ref, stored.FullReference())
	require.Empty(t, EvidenceReferencesFromInternal([]interface{}{map[string]interface{}{"id": "ev_test"}}))
}
