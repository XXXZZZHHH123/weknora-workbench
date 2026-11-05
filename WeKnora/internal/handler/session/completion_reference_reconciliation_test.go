package session

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCompletionExplicitEmptyReferencesReconcileLiveAndPersistedHistory(t *testing.T) {
	for _, mode := range []string{"completed_explicit_empty", "completed_nil", "suspended_explicit_empty"} {
		t.Run(mode, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
			repo := repository.NewMessageRepository(db)
			message := &types.Message{SessionID: "session", Role: "assistant"}
			_, err = repo.CreateMessage(t.Context(), message)
			require.NoError(t, err)
			stream := &completionEventRecorder{}
			h := NewAgentStreamHandler(context.Background(), message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, nil, nil, nil, nil)
			read := &types.SearchResult{ID: "actual-read", KnowledgeID: "doc", KnowledgeBaseID: "kb", Content: "Actual read passage."}
			require.NoError(t, h.handleReferences(t.Context(), event.Event{Data: event.AgentReferencesData{References: []*types.SearchResult{read}}}))
			require.Len(t, message.KnowledgeReferences, 1)
			data := event.AgentCompleteData{MessageID: message.ID, FinalAnswer: "Answer with an honest review warning."}
			if mode != "completed_nil" {
				data.KnowledgeRefs = []interface{}{}
				data.EvidenceRefs = []interface{}{}
			}
			if mode == "suspended_explicit_empty" {
				data.Suspended = &types.PromptSuspension{PromptID: "approval", Kind: "approval"}
			}
			require.NoError(t, h.handleComplete(t.Context(), event.Event{Data: data}))
			if mode == "completed_explicit_empty" {
				require.Empty(t, message.KnowledgeReferences)
				require.Empty(t, h.knowledgeRefs)
				require.Empty(t, h.completionEvent.Data["knowledge_references"])
				require.Empty(t, h.completionEvent.Data["evidence_references"])
				require.Contains(t, h.completionEvent.Data, "knowledge_references", "an explicit empty array reconciles previously streamed navigation")
				require.Contains(t, h.completionEvent.Data, "evidence_references")
			} else {
				require.Len(t, message.KnowledgeReferences, 1, "legacy nil and suspension keep their checkpoint semantics")
				require.NotContains(t, h.completionEvent.Data, "knowledge_references")
			}
			require.NoError(t, repo.UpdateMessage(t.Context(), message))
			require.NoError(t, h.publishCompletion(t.Context()))
			reloaded, err := repo.GetMessage(t.Context(), message.SessionID, message.ID)
			require.NoError(t, err)
			require.Equal(t, message.KnowledgeReferences, reloaded.KnowledgeReferences)
			require.Equal(t, message.Content, reloaded.Content)
			if mode == "completed_explicit_empty" {
				last := stream.events[len(stream.events)-1]
				require.Equal(t, types.ResponseTypeComplete, last.Type)
				require.Contains(t, last.Data, "knowledge_references")
				require.Equal(t, reloaded.Content, last.Data["final_content"])
			}
		})
	}
}
