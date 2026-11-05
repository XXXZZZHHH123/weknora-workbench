package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEvidenceCompletionRetainsFullRecordsButPublishesOnlyAfterCommit(t *testing.T) {
	message := &types.Message{ID: "message", SessionID: "session", Role: "assistant"}
	stream := &completionEventRecorder{}
	h := NewAgentStreamHandler(context.Background(), message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, event.NewEventBus(), nil, nil, nil)
	const answer = "Bound fact. <evidence evidence_id=\"ev_fact\" />"
	ref := types.EvidenceReference{ID: "ev_fact", Kind: types.EvidenceKindRawCell, ResultID: "result", ResultHash: strings.Repeat("a", 64), CandidateHash: types.EvidenceCandidateHash(answer), Sources: []types.EvidenceSourceSnapshot{{KnowledgeBaseID: "kb", KnowledgeID: "doc", SourceSHA256: strings.Repeat("b", 64)}}, Manifest: json.RawMessage(`{"result":{"columns":["PRIVATE_MANIFEST"],"rows":[[1]]}}`)}
	require.NoError(t, h.handleReferences(t.Context(), event.Event{Data: event.AgentReferencesData{EvidenceReferences: types.EvidenceReferences{ref.PublicSummary()}}}))
	for _, saved := range stream.events {
		require.NotContains(t, saved.Data, "evidence_references", "an uncommitted reference must not become a clickable delivery")
	}
	data := event.AgentCompleteData{MessageID: message.ID, FinalAnswer: answer, EvidenceRefs: []interface{}{ref}}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE_MANIFEST")
	require.NoError(t, h.handleComplete(t.Context(), event.Event{Data: data}))
	require.Len(t, message.EvidenceReferences, 1)
	require.Equal(t, ref.Manifest, message.EvidenceReferences[0].Manifest)
	require.NotNil(t, h.completionEvent)
	for _, saved := range stream.events {
		require.NotEqual(t, types.ResponseTypeComplete, saved.Type)
	}
	// The outer pipeline calls this only after its message transaction commits.
	require.NoError(t, h.publishCompletion(t.Context()))
	completed := stream.events[len(stream.events)-1]
	raw, err = json.Marshal(completed.Data)
	require.NoError(t, err)
	require.Contains(t, string(raw), "ev_fact")
	require.NotContains(t, string(raw), "PRIVATE_MANIFEST")
	require.NotContains(t, string(raw), "sources")
}

func TestEvidenceCompletionPreservesApprovedCandidateWithArtifactsAndHistory(t *testing.T) {
	old := types.MessageArtifact{URL: "resource://" + strings.Repeat("a", 22), FileName: "figure.png", FileType: ".png", SourcePath: "/workspace/output/figure.png"}
	next := old
	next.URL = "resource://" + strings.Repeat("b", 22)
	for _, scenario := range []struct {
		name       string
		candidate  string
		attached   types.MessageArtifacts
		collector  *service.ArtifactCollector
		wantFiles  int
		incomplete bool
	}{
		{name: "canonical_resource", candidate: "![Figure](" + next.URL + ")", attached: types.MessageArtifacts{next}, wantFiles: 1},
		{name: "sandbox_reference", candidate: "![Figure](sandbox:figure.png)", attached: types.MessageArtifacts{next}, wantFiles: 1},
		{name: "prior_version", candidate: "![Previous](" + old.URL + ")", attached: types.MessageArtifacts{next}, wantFiles: 2,
			collector: service.NewArtifactCollector(nil, nil, completionHistory{[]types.MessageArtifact{old}}, nil, service.ArtifactCollectorConfig{})},
		{name: "partial_delivery", candidate: "![Figure](sandbox:figure.png)", attached: types.MessageArtifacts{next}, wantFiles: 1, incomplete: true,
			collector: service.NewArtifactCollector(unavailableArtifactSource{}, &fakeArtifactFileService{}, nil, nil, service.ArtifactCollectorConfig{OutputDir: "/workspace/output"})},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
			repo := repository.NewMessageRepository(db)
			message := &types.Message{SessionID: "session", Role: "assistant"}
			_, err = repo.CreateMessage(t.Context(), message)
			require.NoError(t, err)
			message.Artifacts = scenario.attached
			candidate := scenario.candidate + " <evidence evidence_id=\"ev_artifact_fact\" />"
			ref := types.EvidenceReference{ID: "ev_artifact_fact", Kind: types.EvidenceKindRawCell, ResultID: "result", ResultHash: strings.Repeat("c", 64),
				CandidateHash: types.EvidenceCandidateHash(candidate), ArtifactIDs: []string{next.URL}, Sources: []types.EvidenceSourceSnapshot{{KnowledgeBaseID: "kb", KnowledgeID: "doc", SourceSHA256: strings.Repeat("d", 64)}},
				Manifest: json.RawMessage(`{"result":{"columns":["value"],"rows":[[27]]}}`)}
			stream := &completionEventRecorder{}
			h := NewAgentStreamHandler(context.Background(), message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, nil, scenario.collector, nil, nil)
			require.NoError(t, h.handleComplete(t.Context(), event.Event{Data: event.AgentCompleteData{MessageID: message.ID, FinalAnswer: candidate, EvidenceRefs: []interface{}{ref}}}))
			require.Equal(t, candidate, message.Content, "service delivery cannot amend a reviewed evidence candidate")
			require.Equal(t, ref.CandidateHash, types.EvidenceCandidateHash(message.Content))
			require.Len(t, message.Artifacts, scenario.wantFiles)
			require.Equal(t, scenario.incomplete, message.ArtifactDeliveryIncomplete)
			require.NoError(t, repo.UpdateMessage(t.Context(), message), "actual message/evidence/artifact transaction must commit")
			require.NoError(t, h.publishCompletion(t.Context()))
			last := stream.events[len(stream.events)-1]
			require.Equal(t, candidate, last.Data["final_content"])
			require.Equal(t, publicArtifactViews(message.Artifacts), last.Data["artifacts"])
			if scenario.incomplete {
				require.Equal(t, true, last.Data["artifact_delivery_incomplete"])
			} else {
				require.NotContains(t, last.Data, "artifact_delivery_incomplete")
			}
			reloaded, err := repo.GetMessagesBySession(t.Context(), message.SessionID, 1, 10)
			require.NoError(t, err)
			require.Len(t, reloaded, 1)
			require.Equal(t, candidate, reloaded[0].Content)
			require.Equal(t, message.EvidenceReferences, reloaded[0].EvidenceReferences)
			require.Len(t, reloaded[0].Artifacts, scenario.wantFiles)
			require.Equal(t, scenario.incomplete, reloaded[0].ArtifactDeliveryIncomplete, "delivery warning survives history refresh independently of the approved content")
		})
	}
}
