package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	"github.com/Tencent/WeKnora/internal/stream"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type interruptionKBs struct {
	interfaces.KnowledgeBaseService
	base *types.KnowledgeBase
}

func (kb interruptionKBs) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if kb.base == nil || id != kb.base.ID {
		return nil, nil
	}
	return kb.base, nil
}

func (kb interruptionKBs) GetKnowledgeBasesByIDsOnly(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	var result []*types.KnowledgeBase
	for _, id := range ids {
		if kb.base != nil && id == kb.base.ID {
			result = append(result, kb.base)
		}
	}
	return result, nil
}

type interruptionKnowledge struct {
	interfaces.KnowledgeService
	docs map[string]*types.Knowledge
}

func (k interruptionKnowledge) GetKnowledgeByID(_ context.Context, id string) (*types.Knowledge, error) {
	return k.docs[id], nil
}

func (k interruptionKnowledge) GetKnowledgeBatchWithSharedAccess(_ context.Context, _ uint64, ids []string) ([]*types.Knowledge, error) {
	var documents []*types.Knowledge
	for _, id := range ids {
		if document := k.docs[id]; document != nil {
			documents = append(documents, document)
		}
	}
	return documents, nil
}

type interruptionChunks struct {
	interfaces.ChunkService
	chunk *types.Chunk
}

func (k interruptionChunks) GetChunkByIDOnly(_ context.Context, id string) (*types.Chunk, error) {
	if k.chunk != nil && k.chunk.ID == id {
		return k.chunk, nil
	}
	return nil, nil
}

type interruptedSessionFixture struct {
	collaborationSessions
	service *sessionService
}

func (s *interruptedSessionFixture) InterruptedSourceAuthorization(ctx context.Context, message *types.Message, durable bool) func(agent.InterruptedSource) bool {
	return s.service.InterruptedSourceAuthorization(ctx, message, durable)
}

func TestAf1KB28InterruptionRecoversOnlyAuthorizedCurrentReceipts(t *testing.T) {
	// Actual A-2 #26, #33, #60 and #65 from the persisted assistant steps.
	// The historical read retains the page/table location but omits OCR text.
	data, err := os.ReadFile("../../agent/testdata/qa4/replay/af1-kb28-r2-interruption.json")
	require.NoError(t, err)
	var trace struct {
		Steps           []types.AgentStep `json:"steps"`
		HistoricalFinal string            `json:"historical_final"`
	}
	require.NoError(t, json.Unmarshal(data, &trace))
	doc := trace.Steps[0].ToolCalls[0].Result.Data
	workbook := trace.Steps[1].ToolCalls[0].Result.Data["source"].(map[string]any)
	kbID, reportID, databaseID := doc["knowledge_base_id"].(string), doc["knowledge_id"].(string), workbook["knowledge_id"].(string)
	for _, switches := range [][2]string{{"false", "false"}, {"false", "true"}, {"true", "false"}, {"true", "true"}} {
		for _, scenario := range []string{"persisted steps", "checkpoint after restart", "revoked document grant", "changed workbook", "swapped workbook identity", "lost explicit selection", "steer clears prior task"} {
			t.Run(switches[0]+"/"+switches[1]+"/"+scenario, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", switches[0])
				t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", switches[1])
				s, _, messages, ctx := collaborationService(t)
				base := &types.KnowledgeBase{ID: kbID, TenantID: 7}
				report := &types.Knowledge{ID: reportID, KnowledgeBaseID: kbID, TenantID: 7, ParseStatus: types.ParseStatusCompleted}
				book := &types.Knowledge{ID: databaseID, KnowledgeBaseID: kbID, TenantID: 7, ParseStatus: types.ParseStatusCompleted, FileHash: workbook["file_hash"].(string)}
				docs := map[string]*types.Knowledge{reportID: report, databaseID: book}
				chunk := &types.Chunk{ID: doc["source_chunk_id"].(string), IsEnabled: true, KnowledgeID: reportID, KnowledgeBaseID: kbID, TenantID: 7}
				s.Bind(&interruptedSessionFixture{service: &sessionService{knowledgeBaseService: interruptionKBs{base: base}, knowledgeService: interruptionKnowledge{docs: docs}, chunkService: interruptionChunks{chunk: chunk}}})
				s.streams = stream.NewMemoryStreamManager()
				scope := collaboration.Scope{TenantID: 7, UserID: "u", SessionID: "parent", ID: "answer"}
				b, err := s.Runtime.AcquireParent(ctx, scope)
				require.NoError(t, err)
				message := &types.Message{ID: scope.ID, SessionID: scope.SessionID, Role: "assistant", Content: "unpublished draft 331.2", AgentSteps: trace.Steps,
					ExecutionContext: types.MessageExecutionContext{KnowledgeBaseIDs: []string{kbID}, Locale: "zh-CN"}}
				switch scenario {
				case "checkpoint after restart", "lost explicit selection":
					require.NoError(t, b.RecordParentEvidence(ctx, message.ID, agent.InterruptedEvidenceFromSteps(trace.Steps)))
					message.AgentSteps = nil // Simulate death before the terminal transcript write.
					if scenario == "lost explicit selection" {
						message.ExecutionContext.KnowledgeBaseIDs = nil
						message.ExecutionContext.KnowledgeIDs = []string{"no-longer-readable"}
					}
				case "revoked document grant":
					delete(docs, reportID)
				case "changed workbook":
					book.FileHash = "changed"
				case "swapped workbook identity":
					book.ID = "different-physical-source"
				case "steer clears prior task":
					require.NoError(t, b.RecordParentEvidence(ctx, message.ID, agent.InterruptedEvidenceFromSteps(trace.Steps)))
					require.NoError(t, b.ClearParentEvidence(ctx, message.ID))
				}
				require.NoError(t, messages.UpdateMessage(ctx, message))
				require.NoError(t, s.Runtime.Store.Change(ctx, scope, func(g *collaboration.Group) error {
					g.ParentLease = time.Now().Add(-time.Second)
					return nil
				}))
				// A new runtime has no local owner and does not replay the model.
				s.Runtime = collaboration.New(collaboration.NewStore(s.db), nil)
				closed := make(chan error, 1)
				s.Runtime.WakeParent = func(ctx context.Context, b *collaboration.Binding, snapshot []byte) error {
					var err error
					if len(snapshot) > 0 {
						t.Error("unexpected model recovery checkpoint")
					}
					err = s.finishInterruptedParent(ctx, b)
					closed <- err
					return err
				}
				startCollaborationTestRuntime(ctx, t, s.Runtime)
				select {
				case err := <-closed:
					require.NoError(t, err)
				case <-time.After(2 * time.Second):
					t.Fatal("orphan recovery did not publish")
				}
				stored, err := messages.GetMessage(ctx, scope.SessionID, scope.ID)
				require.NoError(t, err)
				require.Equal(t, types.MessageRunStateInterrupted, stored.RunState)
				require.True(t, stored.IsCompleted)
				require.NotContains(t, stored.Content, trace.HistoricalFinal)
				require.NotContains(t, stored.Content, "331.2")
				if scenario != "changed workbook" && scenario != "swapped workbook identity" && scenario != "lost explicit selection" && scenario != "steer clears prior task" {
					require.Contains(t, stored.Content, "298.93291576470585")
					require.Contains(t, stored.Content, "G87")
					require.Contains(t, stored.Content, "2023-10-31")
				} else {
					require.NotContains(t, stored.Content, "298.93291576470585")
				}
				if scenario != "revoked document grant" && scenario != "lost explicit selection" && scenario != "steer clears prior task" {
					require.Contains(t, stored.Content, "第 6 页")
				} else {
					require.NotContains(t, stored.Content, reportID)
					require.NotContains(t, stored.Content, doc["knowledge_title"])
				}
				events, _, err := s.streams.GetEvents(ctx, scope.SessionID, scope.ID, 0)
				require.NoError(t, err)
				require.Len(t, events, 2)
				for i, kind := range []types.ResponseType{types.ResponseTypeError, types.ResponseTypeComplete} {
					require.Equal(t, kind, events[i].Type)
					require.Equal(t, stored.Content, events[i].Content)
					require.Equal(t, stored.Content, events[i].Data["final_content"])
					require.Equal(t, false, events[i].Data["success"])
				}
			})
		}
	}
}
