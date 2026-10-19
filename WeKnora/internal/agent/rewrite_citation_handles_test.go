package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func rewriteCitationSourceFixture() (map[string]any, string) {
	data := map[string]any{
		"display_type":    "knowledge_chunks_list",
		"knowledge_id":    "north-report",
		"knowledge_title": "North watershed survey",
		"document": map[string]any{
			"knowledge_id": "north-report", "title": "North watershed survey",
		},
		"chunks": []map[string]any{
			{
				"chunk_id": "north-flow-chunk", "knowledge_id": "north-report",
				"knowledge_base_id": "watershed-kb", "knowledge_title": "North watershed survey",
				"content": "The north gauge recorded stable flow.", "content_complete": true,
			},
			{
				"chunk_id": "south-rainfall-chunk", "knowledge_id": "south-report",
				"knowledge_base_id": "rainfall-kb", "knowledge_title": "South rainfall note",
				"content": "The south gauge recorded delayed rainfall runoff.", "content_complete": true,
			},
		},
	}
	payload, _ := json.Marshal(map[string]any{"success": true, "data": data})
	return data, string(payload)
}

func rewriteCitationReviewInput(t *testing.T, messages []chat.Message) map[string]any {
	t.Helper()
	require.Len(t, messages, 2)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &input))
	return input
}

func TestIndependentReviewCitationHandleRoundTripUsesExactCurrentChunk(t *testing.T) {
	question := "Compare the north gauge with the south rainfall note."
	draft := `The north report was also retrieved for comparison. South runoff was delayed <kb doc="South rainfall note" chunk_id="south-rainfall-chunk" kb_id="rainfall-kb" />. Historical navigation <kb doc="Old field log" chunk_id="historical-only-chunk" kb_id="old-kb" />. Unseen note <kb doc="Unfetched note" chunk_id="never-fetched-chunk" kb_id="other-kb" />.`
	data, rawSource := rewriteCitationSourceFixture()
	sourceMessage := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "read-two-reports", Content: rawSource}
	conversation := []chat.Message{{Role: "user", Content: question}}
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      `The cited south finding is supported <ref id="c2"/>; navigation only <ref id="c3"/>; unknown <ref id="c4"/> and <ref id="c999"/>.`,
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)

	// Seed this request's registry from one successful tool result containing
	// two distinct real chunks. The second chunk must remain the cited source.
	rendered := engine.modelContext.ModelToolResultForTool("read_document", &types.ToolResult{Success: true, Data: data})
	require.Contains(t, rendered, `<chunk id="c1"`)
	require.Contains(t, rendered, `<chunk id="c2"`)
	require.Equal(t, "c1", engine.modelContext.CitableChunkHandle("north-flow-chunk"))
	require.Equal(t, "c2", engine.modelContext.CitableChunkHandle("south-rainfall-chunk"))
	historyHandle := engine.modelContext.RegisterContextChunk(modelcontext.ChunkReference{
		ChunkID: "historical-only-chunk", KnowledgeID: "old-report", KnowledgeBaseID: "old-kb", DocumentTitle: "Old field log",
	})
	require.Equal(t, "c3", historyHandle)
	require.Empty(t, engine.modelContext.CitableChunkHandle("historical-only-chunk"), "navigation registration cannot substantiate an answer")

	review := independentStructuredReviewWithSourceCompanions(
		[]chat.Message{{Role: "system", Content: "Review only the actual evidence."}, sourceMessage},
		conversation,
		nil, nil, nil, question, draft,
	)
	originalReview := append([]chat.Message(nil), review...)
	before := rewriteCitationReviewInput(t, review)
	bound := engine.bindIndependentReviewCitations(review, []chat.Message{sourceMessage})
	require.Equal(t, originalReview, review, "binding must not mutate the shared review transcript")
	input := rewriteCitationReviewInput(t, bound)

	boundDraft, ok := input["unverified_draft"].(string)
	require.True(t, ok)
	require.Contains(t, boundDraft, `<ref id="c2"/>`, "the draft's second real chunk must map to its exact registered handle")
	require.Contains(t, boundDraft, `<ref id="c3"/>`, "historical navigation can be represented but must not become citable")
	unknownHandle := engine.modelContext.ChunkHandle("never-fetched-chunk")
	require.NotEmpty(t, unknownHandle)
	require.Contains(t, boundDraft, `<ref id="`+unknownHandle+`"/>`)
	require.Empty(t, engine.modelContext.CitableChunkHandle("never-fetched-chunk"), "re-encoding a draft cannot promote an unknown ID")

	bindingsView, ok := input["source_citation_handles"].(map[string]any)
	require.True(t, ok)
	bindings, ok := bindingsView["bindings"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, 2, "the complete current source directory retains both authorized chunks")
	binding := bindings[0].(map[string]any)
	require.Equal(t, "south-rainfall-chunk", binding["chunk_id"])
	require.Equal(t, "c2", binding["citation_handle"])
	require.NotEmpty(t, binding["evidence_id"])
	require.Equal(t, "/data/chunks/1/content", binding["json_pointer"])
	other := bindings[1].(map[string]any)
	require.Equal(t, "north-flow-chunk", other["chunk_id"])
	require.Equal(t, "c1", other["citation_handle"])
	require.Equal(t, "/data/chunks/0/content", other["json_pointer"])
	require.NotContains(t, fmt.Sprint(bindingsView), "historical-only-chunk")
	require.NotContains(t, fmt.Sprint(bindingsView), "never-fetched-chunk")

	// The wrapper changes only the draft and adds a bounded handle side table.
	// The immutable payload and exact pointer evidence remain the same.
	require.Equal(t, before["source_tool_results"], input["source_tool_results"])
	require.Equal(t, before["citation_source_evidence"], input["citation_source_evidence"])
	encodedSource := fmt.Sprint(input["source_tool_results"])
	require.Contains(t, encodedSource, "The south gauge recorded delayed rainfall runoff.")
	require.Contains(t, encodedSource, "The north gauge recorded stable flow.", "uncited but successfully fetched source evidence remains available")

	// Run the actual mock stream path: the model copies c2 back, while an old
	// navigation handle, a newly registered draft-only ID, and a forged handle
	// must not decode into citable canonical citations.
	streamed, err := engine.streamLLMToEventBus(context.Background(), bound, nil, nil)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	wireInput := rewriteCitationReviewInput(t, model.calls[0])
	wireDraft, _ := wireInput["unverified_draft"].(string)
	require.Contains(t, wireDraft, `<ref id="c2"/>`)
	require.Equal(t, input["source_tool_results"], wireInput["source_tool_results"])
	require.Equal(t, input["source_citation_handles"], wireInput["source_citation_handles"])
	require.Contains(t, streamed.Content, `chunk_id="south-rainfall-chunk"`)
	require.NotContains(t, streamed.Content, "north-flow-chunk")
	require.NotContains(t, streamed.Content, "historical-only-chunk")
	require.NotContains(t, streamed.Content, "never-fetched-chunk")
	require.NotContains(t, streamed.Content, "c999")
}

func TestIndependentReviewCitationHandleViewEnforcesItsOwnBudgetAndCounts(t *testing.T) {
	const realChunkCount = 128
	engine := newTestEngine(t, &mockChat{})
	items := make([]any, 0, realChunkCount+1)
	sourceRows := make([]any, 0, realChunkCount)
	var draft strings.Builder
	for i := 0; i < realChunkCount; i++ {
		id := fmt.Sprintf("current-source-%03d-%s", i, strings.Repeat("x", 88))
		knowledgeID := fmt.Sprintf("document-%03d", i)
		draft.WriteString(`<kb doc="Field study" chunk_id="` + id + `" kb_id="research-kb" /> `)
		items = append(items, map[string]any{
			"chunk_id": id,
			"continuation": map[string]any{
				"evidence_id":  fmt.Sprintf("evidence-%03d", i),
				"json_pointer": fmt.Sprintf("/data/chunks/%d/content", i),
			},
		})
		sourceRows = append(sourceRows, map[string]any{
			"chunk_id": id, "knowledge_id": knowledgeID, "knowledge_base_id": "research-kb",
			"knowledge_title": "Field study", "content": fmt.Sprintf("Rainfall observation %03d", i),
		})
	}
	historyID := "historical-not-citable"
	historyHandle := engine.modelContext.RegisterContextChunk(modelcontext.ChunkReference{
		ChunkID: historyID, KnowledgeID: "old-report", KnowledgeBaseID: "old-kb", DocumentTitle: "Old field log",
	})
	require.Equal(t, "c1", historyHandle)
	draft.WriteString(`<kb doc="Old field log" chunk_id="` + historyID + `" kb_id="old-kb" />`)
	items = append(items, map[string]any{
		"chunk_id":     historyID,
		"continuation": map[string]any{"evidence_id": "old-evidence", "json_pointer": "/data/chunks/0/content"},
	})
	sourceData := map[string]any{
		"display_type": "knowledge_chunks_list", "knowledge_base_id": "research-kb", "chunks": sourceRows,
	}
	engine.modelContext.ModelToolResultForTool("read_document", &types.ToolResult{Success: true, Data: sourceData})
	sourcePayload, err := json.Marshal(map[string]any{"success": true, "data": sourceData})
	require.NoError(t, err)
	sourceMessages := []chat.Message{{Role: "tool", Name: "read_document", ToolCallID: "current-source-batch", Content: string(sourcePayload)}}
	inputBytes, err := json.Marshal(map[string]any{
		"unverified_draft":         draft.String(),
		"citation_source_evidence": map[string]any{"items": items},
	})
	require.NoError(t, err)
	messages := []chat.Message{{Role: "system", Content: "Review source mappings."}, {Role: "user", Content: string(inputBytes)}}
	original := append([]chat.Message(nil), messages...)
	bound := engine.bindIndependentReviewCitations(messages, sourceMessages)
	require.Equal(t, original, messages, "binding is a projection and does not rewrite the source conversation")
	projected := rewriteCitationReviewInput(t, bound)
	view, ok := projected["source_citation_handles"].(map[string]any)
	require.True(t, ok)
	encodedView, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encodedView), rewriteCitationHandlesBudget)
	require.Equal(t, float64(rewriteCitationHandlesBudget), view["budget_bytes"])
	available := int(view["available_binding_count"].(float64))
	included := int(view["included_binding_count"].(float64))
	omitted := int(view["omitted_binding_count"].(float64))
	unavailable := int(view["unavailable_binding_count"].(float64))
	require.Equal(t, realChunkCount, available)
	require.Equal(t, 1, unavailable, "an old or unknown draft citation is counted but never receives a handle")
	require.Equal(t, available, included+omitted, "binding truncation accounting must remain exact")
	require.Greater(t, omitted, 0, "the fixture should exceed the strict handle-view budget")
	require.False(t, view["projection_complete"].(bool))
	require.Empty(t, engine.modelContext.CitableChunkHandle(historyID))

	// JSON payloads and existing evidence paths are not charged as new body data
	// or rewritten when the compact mapping table is shortened.
	require.Equal(t, string(inputBytes), messages[1].Content)
	require.NotContains(t, string(encodedView), "historical-not-citable")
}
