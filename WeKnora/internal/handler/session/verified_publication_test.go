package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestVerifiedPublicationStoresTheProjectedAnswerAndOriginalEvidence(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
	ctx := t.Context()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
	repo := repository.NewMessageRepository(db)
	msg := &types.Message{SessionID: "session", Role: "assistant"}
	_, err = repo.CreateMessage(ctx, msg)
	require.NoError(t, err)
	const canonical = `数量🙂 42<evidence evidence_id="ev_cell" />，原值按原表记录。`
	const published = "数量🙂 42〔原始台账.xlsx〕，原值按原表记录。"
	ref := types.EvidenceReference{
		ID: "ev_cell", Kind: types.EvidenceKindRawCell, Title: "原始台账.xlsx", ResultID: "observation", ResultHash: strings.Repeat("a", 64),
		CandidateHash: types.EvidenceCandidateHash(canonical),
		Sources:       []types.EvidenceSourceSnapshot{{KnowledgeBaseID: "private-kb", KnowledgeID: "private-source", SourceSHA256: strings.Repeat("b", 64)}},
		Manifest:      json.RawMessage(`{"result":{"rows":[[42]],"columns":["PRIVATE_MANIFEST"]}}`),
	}
	stream, bus := &completionEventRecorder{}, event.NewEventBus()
	h := NewAgentStreamHandler(ctx, msg.SessionID, msg.ID, "run", 7, time.Time{}, msg, stream, bus, nil, nil, nil)
	h.Subscribe()

	require.NoError(t, bus.Emit(ctx, event.Event{ID: "complete", Type: event.EventAgentComplete, Data: event.AgentCompleteData{
		MessageID: msg.ID, FinalAnswer: canonical, EvidenceRefs: []interface{}{ref},
	}}))
	require.Len(t, stream.events, 2, "the complete frame must wait for durable evidence")
	require.Equal(t, published, stream.events[0].Content)
	require.NoError(t, repo.UpdateMessage(ctx, msg))
	require.NoError(t, h.publishCompletion(ctx))

	reloaded, err := repo.GetMessage(ctx, msg.SessionID, msg.ID)
	require.NoError(t, err)
	require.Equal(t, published, reloaded.Content)
	require.Len(t, reloaded.EvidenceReferences, 1)
	stored := reloaded.EvidenceReferences[0]
	require.Equal(t, ref.ID, stored.ID)
	require.Equal(t, ref.Manifest, stored.Manifest)
	require.Equal(t, ref.Sources, stored.Sources)
	require.NotEmpty(t, stored.ClaimBindings)
	require.Equal(t, types.EvidenceCandidateHash(published), stored.CandidateHash)
	complete := stream.events[2]
	require.Equal(t, published, complete.Data["final_content"])
	raw, err := json.Marshal(buildStreamResponse(complete, "request"))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE_MANIFEST")
	require.NotContains(t, string(raw), "private-source")
	require.NotContains(t, string(raw), "private-kb")
	require.Contains(t, string(raw), ref.ID)
}

func TestVerifiedEvidencePartialReplaysThePersistedAnswerWithoutAnErrorFrame(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	ctx := t.Context()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
	repo := repository.NewMessageRepository(db)
	msg := &types.Message{SessionID: "session", Role: "assistant", RequestID: "r"}
	_, err = repo.CreateMessage(ctx, msg)
	require.NoError(t, err)
	const supported = "The selected ledger contains the March entry. The archived report contains only the quarterly total."
	const reason = "answer_evidence_missing"
	stream, bus := &completionEventRecorder{}, event.NewEventBus()
	h := NewAgentStreamHandler(ctx, msg.SessionID, msg.ID, "run", 7, time.Time{}, msg, stream, bus, nil, nil, nil)
	h.Subscribe()
	require.NoError(t, bus.Emit(ctx, event.Event{ID: "complete", Type: event.EventAgentComplete, Data: event.AgentCompleteData{
		MessageID: msg.ID, FinalAnswer: supported, Incomplete: &event.AgentIncompleteData{Reason: reason, Notice: "Private detail", PartialAnswer: true},
	}}))
	require.NoError(t, repo.UpdateMessage(ctx, msg))
	require.NoError(t, h.publishCompletion(ctx))
	require.Len(t, stream.events, 3)
	require.Equal(t, supported, stream.events[0].Content)
	require.Equal(t, supported, stream.events[2].Data["final_content"])
	require.Equal(t, types.MessageRunStateEvidencePartial, stream.events[2].Data["run_state"])
	require.Equal(t, reason, stream.events[2].Data["code"])
	require.NotContains(t, stream.events[2].Data, "success")
	require.NotContains(t, stream.events[2].Data, "error")

	reloaded, err := repo.GetMessage(ctx, msg.SessionID, msg.ID)
	require.NoError(t, err)
	require.Equal(t, types.MessageRunStateEvidencePartial, reloaded.RunState)
	require.Equal(t, reason, types.PublicAgentMessage(reloaded).RunReason)
	stale := []interfaces.StreamEvent{
		{Type: types.ResponseTypeAnswer, Content: "Superseded draft"},
		{Type: types.ResponseTypeError, Content: "Superseded error"},
	}
	for _, cached := range [][]interfaces.StreamEvent{nil, stale, stream.events} {
		handler := &Handler{sessionService: &stubSessionService{}, messageService: &incompleteReplayMessageService{message: reloaded}, streamManager: &stubStreamManager{events: cached}}
		router := gin.New()
		router.GET("/replay/:session_id", handler.ContinueStream)
		writer := httptest.NewRecorder()
		router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/replay/"+msg.SessionID+"?message_id="+msg.ID, nil))
		require.Equal(t, http.StatusOK, writer.Code)
		wire := writer.Body.String()
		require.NotContains(t, wire, "Private detail")
		require.NotContains(t, wire, "Superseded")
		require.NotContains(t, wire, `"response_type":"error"`)
		require.Contains(t, wire, `"response_type":"answer"`)
		require.Contains(t, wire, `"code":"`+reason+`"`)
		require.Contains(t, wire, `"final_content":"`+supported+`"`)
	}
}

func TestVerifiedPublicationReplayWithholdsSplitLegacyTagsAndHonorsReplacement(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
	const canonical = `The observed value was 42.<kb doc="Read document" chunk_id="trusted" /> Further prose.`
	const published = "The observed value was 42.〔Read document〕 Further prose."
	const safeReplacement = "The user's corrected request has a new answer."
	for _, completed := range []bool{true, false} {
		t.Run(map[bool]string{true: "saved-answer", false: "pending-stream"}[completed], func(t *testing.T) {
			ref := &types.SearchResult{ID: "trusted", KnowledgeTitle: "Read document"}
			message := &types.Message{ID: "m", SessionID: "s", RequestID: "r", AgentID: "agent", Role: "assistant",
				IsCompleted: completed, KnowledgeReferences: types.References{ref}}
			content := canonical
			if completed {
				message.Content = canonical
			} else {
				content = safeReplacement
			}
			events := []interfaces.StreamEvent{
				{ID: "answer", Type: types.ResponseTypeAnswer, Content: "The observed value was 42.<k", Data: map[string]any{"event_id": "answer"}},
				{ID: "activity", Type: types.ResponseTypeThinking, Content: "Relevant source selected."},
				{ID: "answer", Type: types.ResponseTypeAnswer, Content: `b doc="Read document" chunk_id="trusted" /> Further prose.`, Data: map[string]any{"event_id": "answer"}},
			}
			if !completed {
				events = append(events, interfaces.StreamEvent{ID: "answer", Type: types.ResponseTypeAnswer, Content: safeReplacement, Data: map[string]any{"event_id": "answer", "replace": true}})
			}
			events = append(events,
				interfaces.StreamEvent{ID: "answer", Type: types.ResponseTypeAnswer, Done: true, Data: map[string]any{"event_id": "answer"}},
				interfaces.StreamEvent{ID: "complete", Type: types.ResponseTypeComplete, Done: true, Data: map[string]any{"final_content": content}},
			)
			handler := &Handler{sessionService: &stubSessionService{}, messageService: &incompleteReplayMessageService{message: message}, streamManager: &stubStreamManager{events: events}}
			router := gin.New()
			router.GET("/replay/:session_id", handler.ContinueStream)
			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/replay/s?message_id=m", nil))
			require.Equal(t, http.StatusOK, writer.Code)

			var answer string
			for _, line := range strings.Split(writer.Body.String(), "\n") {
				encoded, ok := strings.CutPrefix(line, "data:")
				if !ok {
					continue
				}
				var frame types.StreamResponse
				require.NoError(t, json.Unmarshal([]byte(encoded), &frame))
				switch frame.ResponseType {
				case types.ResponseTypeAnswer:
					require.NotContains(t, frame.Content, "chunk_id")
					require.NotContains(t, frame.Content, "<k")
					if frame.Data["replace"] == true {
						answer = frame.Content
					} else {
						answer += frame.Content
					}
				case types.ResponseTypeComplete:
					require.Equal(t, map[bool]string{true: published, false: safeReplacement}[completed], frame.Data["final_content"])
				}
			}
			require.Equal(t, map[bool]string{true: published, false: safeReplacement}[completed], answer)
			if completed {
				require.Equal(t, canonical, message.Content)
			}
		})
	}
}
