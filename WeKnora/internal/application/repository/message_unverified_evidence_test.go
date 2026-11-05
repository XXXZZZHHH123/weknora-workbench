package repository

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUnverifiedEvidencePersistsExactWarningCandidateWithoutApproval(t *testing.T) {
	repo, _, message := evidenceRepositoryFixture(t)
	message.Content += "\n\n> This answer has not completed automatic review."
	message.EvidenceReferences[0].CandidateHash = types.EvidenceCandidateHash(message.Content)
	message.EvidenceReferences[0].ReviewStatus = "unverified"
	message.EvidenceReferences[0].ClaimBindings = json.RawMessage(`{"protocol":"unverified_navigation_v1","navigation_only":true}`)
	_, err := repo.CreateMessage(t.Context(), message)
	require.NoError(t, err)
	loaded, err := repo.GetMessage(t.Context(), message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, message.Content, loaded.Content)
	require.Equal(t, message.EvidenceReferences, loaded.EvidenceReferences)
	listed, err := repo.GetMessagesBySession(t.Context(), message.SessionID, 1, 10)
	require.NoError(t, err)
	require.Equal(t, loaded.EvidenceReferences, listed[0].EvidenceReferences)
	raw, err := json.Marshal(loaded)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"review_status":"unverified"`)
	require.NotContains(t, string(raw), "navigation_only")
	loaded.Content += " Later substituted words."
	require.Error(t, repo.UpdateMessage(t.Context(), loaded), "unverified navigation still requires the exact saved final candidate")
	unchanged, err := repo.GetMessage(t.Context(), message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, message.Content, unchanged.Content)
	require.Equal(t, message.EvidenceReferences, unchanged.EvidenceReferences)
}
