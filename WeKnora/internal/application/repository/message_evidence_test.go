package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func evidenceRepositoryFixture(t *testing.T) (*messageRepository, *gorm.DB, *types.Message) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
	message := &types.Message{SessionID: "session", Role: "assistant", Content: "Source-bound result.", IsCompleted: true}
	message.EvidenceReferences = types.EvidenceReferences{{ID: "ev_source", Kind: types.EvidenceKindRawCell, ResultID: "actual-result", ResultHash: strings.Repeat("a", 64), CandidateHash: types.EvidenceCandidateHash(message.Content), Sources: []types.EvidenceSourceSnapshot{{KnowledgeBaseID: "kb", KnowledgeID: "doc", SourceSHA256: strings.Repeat("b", 64)}}, Manifest: json.RawMessage(`{"schema_version":1,"result":{"columns":["amount"],"rows":[["27"]]}}`)}}
	return &messageRepository{db: db}, db, message
}

func TestMessageEvidencePersistsAtomicallyAndLoadsWithHistory(t *testing.T) {
	repo, db, message := evidenceRepositoryFixture(t)
	_, err := repo.CreateMessage(context.Background(), message)
	require.NoError(t, err)
	loaded, err := repo.GetMessage(context.Background(), message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, message.EvidenceReferences, loaded.EvidenceReferences)
	listed, err := repo.GetMessagesBySession(context.Background(), message.SessionID, 1, 10)
	require.NoError(t, err)
	require.Equal(t, message.EvidenceReferences, listed[0].EvidenceReferences)
	serialized, err := json.Marshal(loaded)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), `"manifest"`)
	require.NoError(t, repo.UpdateMessage(context.Background(), &types.Message{ID: message.ID, SessionID: message.SessionID, AgentDurationMs: 9}))
	loaded, err = repo.GetMessage(context.Background(), message.SessionID, message.ID)
	require.NoError(t, err)
	require.Len(t, loaded.EvidenceReferences, 1)
	loaded.EvidenceReferences[0].Manifest = json.RawMessage(`{"forged":true}`)
	require.Error(t, repo.UpdateMessage(context.Background(), loaded))
	loaded, err = repo.GetMessage(context.Background(), message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, message.EvidenceReferences, loaded.EvidenceReferences)
	require.NoError(t, repo.DeleteMessage(context.Background(), message.SessionID, message.ID))
	var count int64
	require.NoError(t, db.Model(&types.MessageEvidenceRecord{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestMessageEvidenceRejectsWrongCandidateAndTamperedStorage(t *testing.T) {
	repo, db, message := evidenceRepositoryFixture(t)
	message.EvidenceReferences[0].CandidateHash = strings.Repeat("f", 64)
	_, err := repo.CreateMessage(context.Background(), message)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&types.Message{}).Count(&count).Error)
	require.Zero(t, count, "a final answer with uncommitted evidence is not published")
	message.EvidenceReferences[0].CandidateHash = types.EvidenceCandidateHash(message.Content)
	_, err = repo.CreateMessage(context.Background(), message)
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.MessageEvidenceRecord{}).Where("message_id = ?", message.ID).Update("payload", `{}`).Error)
	_, err = repo.GetMessage(context.Background(), message.SessionID, message.ID)
	require.Error(t, err)
}

func TestMessageEvidenceUpdateRejectsSkippedEmptyContentAtomically(t *testing.T) {
	for _, hasPriorEvidence := range []bool{false, true} {
		name := "legacy_content"
		if hasPriorEvidence {
			name = "existing_evidence"
		}
		t.Run(name, func(t *testing.T) {
			repo, _, message := evidenceRepositoryFixture(t)
			incomingRef := message.EvidenceReferences[0]
			if !hasPriorEvidence {
				message.EvidenceReferences = nil
			}
			_, err := repo.CreateMessage(context.Background(), message)
			require.NoError(t, err)
			before, err := repo.GetMessage(context.Background(), message.SessionID, message.ID)
			require.NoError(t, err)
			incomingRef.ID = "ev_skipped_content"
			incomingRef.CandidateHash = types.EvidenceCandidateHash("")
			update := &types.Message{ID: message.ID, SessionID: message.SessionID, Role: "assistant", IsCompleted: true,
				Content: "", AgentDurationMs: 29, EvidenceReferences: types.EvidenceReferences{incomingRef}}
			require.ErrorContains(t, repo.UpdateMessage(context.Background(), update), "persisted assistant message")
			after, err := repo.GetMessage(context.Background(), message.SessionID, message.ID)
			require.NoError(t, err)
			require.Equal(t, before.Content, after.Content, "GORM skipped empty Content; it must not become the proof candidate")
			require.Equal(t, before.AgentDurationMs, after.AgentDurationMs, "all row changes roll back with rejected evidence")
			require.Equal(t, before.EvidenceReferences, after.EvidenceReferences, "new references do not replace prior evidence on failure")
		})
	}
}

func TestMessageEvidenceForkUsesIndependentMessageIdentity(t *testing.T) {
	repo, _, message := evidenceRepositoryFixture(t)
	_, err := repo.CreateMessage(context.Background(), message)
	require.NoError(t, err)
	copy := *message
	copy.ID = "fork-message"
	copy.SessionID = "fork-session"
	_, err = repo.CreateMessage(context.Background(), &copy)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteMessage(context.Background(), message.SessionID, message.ID))
	loaded, err := repo.GetMessage(context.Background(), copy.SessionID, copy.ID)
	require.NoError(t, err)
	require.Equal(t, copy.EvidenceReferences, loaded.EvidenceReferences)
}
