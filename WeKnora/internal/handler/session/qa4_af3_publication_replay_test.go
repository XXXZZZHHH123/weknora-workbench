package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

var recordedInternalCitation = regexp.MustCompile(`(?i)</?(?:evidence|kb|web)\b`)
var exactRecordedNumber = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

type af3RecordedAnswerEvent struct {
	RequestID string `json:"id"`
	Type      string `json:"response_type"`
	Content   string `json:"content"`
	Done      bool   `json:"done"`
	Data      struct {
		ID            string                   `json:"event_id"`
		Answer        string                   `json:"final_content"`
		EvidenceRefs  types.EvidenceReferences `json:"evidence_references"`
		KnowledgeRefs []*types.SearchResult    `json:"knowledge_references"`
	} `json:"data"`
}

func TestAf3ArchivedPublicCitationReplay(t *testing.T) {
	root := os.Getenv("QA4_AF3_RECORDINGS")
	var compact map[string][]af3RecordedAnswerEvent
	if root == "" {
		raw, err := os.ReadFile("testdata/af3_public_citations.json")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &compact))
	}
	for _, sample := range []struct {
		file        string
		first, last int
		evidence    int
		knowledge   int
		facts       []string
	}{
		{"qa4-Af2-1/KB-07.t1.events.json", 19, 21, 3, 0, []string{"231.93", "110.00", "298.93"}},
		{"qa4-Af2-3/KB-16.t1.events.json", 24, 26, 3, 0, []string{"17110", "2023-09-22"}},
		{"qa4-Af2-1/KB-22.t1.events.json", 28, 30, 4, 0, []string{"17790", "13830", "2023-09-01", "2023-03-16"}},
		{"qa4-Af2-1/KB-31.t1.events.json", 57, 59, 1, 1, []string{"5160", "B3340", "2023年9月21日", "60.6万吨", "棉花周报20240310", "第18页"}},
		{"qa4-Af2-2/KB-31.t1.events.json", 25, 27, 1, 1, []string{"5160", "B3340", "60.6 万吨"}},
	} {
		t.Run(sample.file, func(t *testing.T) {
			recorded := compact[sample.file]
			if root != "" {
				raw, err := os.ReadFile(filepath.Join(root, sample.file))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &recorded))
				require.Len(t, recorded, sample.last)
				recorded = recorded[sample.first-1:]
			}
			require.Len(t, recorded, 3)
			first, close, complete := recorded[0], recorded[1], recorded[2]
			require.Equal(t, "answer", first.Type)
			require.Equal(t, first.Data.ID, close.Data.ID)
			require.True(t, close.Done)
			require.Equal(t, "complete", complete.Type)
			require.Equal(t, first.Content, complete.Data.Answer)
			require.Len(t, complete.Data.EvidenceRefs, sample.evidence)
			require.Len(t, complete.Data.KnowledgeRefs, sample.knowledge)
			for _, fact := range sample.facts {
				require.Contains(t, first.Content, fact)
			}
			t.Logf("request %s; answer #%d–%d (%s), complete #%d", first.RequestID, sample.first, sample.first+1, first.Data.ID, sample.last)

			for _, variant := range []struct {
				name                    string
				verified, live, reverse bool
			}{
				{name: "legacy-live", live: true},
				{name: "legacy-fallback"},
				{name: "public-live", verified: true, live: true},
				{name: "public-fallback", verified: true},
				{name: "public-fallback-reversed", verified: true, reverse: true},
			} {
				t.Run(variant.name, func(t *testing.T) {
					switch {
					case !variant.verified:
						t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
					case variant.reverse:
						t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
					default:
						t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
					}

					evidence := slices.Clone(complete.Data.EvidenceRefs)
					knowledge := slices.Clone(complete.Data.KnowledgeRefs)
					if variant.reverse {
						slices.Reverse(evidence)
						slices.Reverse(knowledge)
					}
					wantContent := first.Content
					if variant.verified {
						wantContent, _ = types.PublicAgentAnswer(first.Content, complete.Data.KnowledgeRefs, complete.Data.EvidenceRefs)
					}
					content := first.Content
					if variant.verified && variant.live {
						content, evidence = types.PublicAgentAnswer(first.Content, knowledge, evidence)
					}
					refs, kb := make([]interface{}, 0, len(evidence)), make([]interface{}, 0, len(knowledge))
					for _, ref := range evidence {
						refs = append(refs, ref)
					}
					for _, ref := range knowledge {
						kb = append(kb, ref)
					}
					ctx := t.Context()
					message := &types.Message{ID: "message", SessionID: "session", Role: "assistant"}
					stream, bus := &completionEventRecorder{}, event.NewEventBus()
					handler := NewAgentStreamHandler(ctx, message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, bus, nil, nil, nil)
					handler.Subscribe()
					if variant.live {
						for _, chunk := range []event.AgentFinalAnswerData{{Content: content}, {Done: true}} {
							require.NoError(t, bus.Emit(ctx, event.Event{ID: first.Data.ID, Type: event.EventAgentFinalAnswer, Data: chunk}))
						}
					}
					require.NoError(t, bus.Emit(ctx, event.Event{ID: "completion", Type: event.EventAgentComplete, Data: event.AgentCompleteData{
						MessageID: message.ID, FinalAnswer: content, EvidenceRefs: refs, KnowledgeRefs: kb,
					}}))
					require.NoError(t, handler.publishCompletion(ctx))

					var visible strings.Builder
					for _, out := range stream.events {
						if out.Type == types.ResponseTypeAnswer {
							if variant.verified {
								require.NotRegexp(t, recordedInternalCitation, out.Content)
							}
							visible.WriteString(buildStreamResponse(out, first.RequestID).Content)
						}
					}
					if variant.verified {
						require.NotRegexp(t, recordedInternalCitation, visible.String())
						for _, reference := range message.EvidenceReferences {
							require.Contains(t, visible.String(), reference.Title)
						}
						for _, reference := range message.KnowledgeReferences {
							require.Contains(t, visible.String(), reference.KnowledgeTitle)
						}
					} else {
						require.Regexp(t, recordedInternalCitation, visible.String())
					}
					for _, fact := range sample.facts {
						if exactRecordedNumber.MatchString(fact) {
							pattern := `(^|[^0-9.])` + regexp.QuoteMeta(fact) + `([^0-9.]|$)`
							require.Regexp(t, pattern, visible.String())
						} else {
							require.Contains(t, visible.String(), fact)
						}
					}
					require.Equal(t, wantContent, visible.String())
					require.Equal(t, visible.String(), message.Content)
					out := stream.events[len(stream.events)-1]
					require.Equal(t, types.ResponseTypeComplete, out.Type)
					require.Equal(t, visible.String(), out.Data["final_content"])
					require.Len(t, message.EvidenceReferences, sample.evidence)
					require.Len(t, message.KnowledgeReferences, sample.knowledge)
					for _, reference := range message.EvidenceReferences {
						require.Equal(t, types.EvidenceCandidateHash(message.Content), reference.CandidateHash)
					}

					historical := &types.Message{
						ID: message.ID, Role: "assistant", AgentID: "agent", IsCompleted: true,
						Content: first.Content, EvidenceReferences: complete.Data.EvidenceRefs, KnowledgeReferences: complete.Data.KnowledgeRefs,
					}
					public := types.PublicAgentMessage(historical)
					require.Equal(t, wantContent, public.Content)
					require.Equal(t, first.Content, historical.Content, "read projection cannot change a persisted evidence hash")
					require.Len(t, public.EvidenceReferences, sample.evidence)
					require.Len(t, public.KnowledgeReferences, sample.knowledge)
					replay := agentReplayPublication{message: historical, public: public}
					wireAnswer := replay.project(interfaces.StreamEvent{Type: types.ResponseTypeAnswer, Content: first.Content})
					wireComplete := replay.project(interfaces.StreamEvent{Type: types.ResponseTypeComplete, Data: map[string]any{
						"final_content": first.Content,
					}})
					require.Equal(t, wantContent, wireAnswer.Content)
					require.Equal(t, wantContent, wireComplete.Data["final_content"])
				})
			}
		})
	}
}
