package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func rewriteDirectorySourceMessage(t *testing.T, engine *AgentEngine, tool, callID, knowledgeID, chunkID, body string, locatorPayload ...string) chat.Message {
	t.Helper()
	chunk := map[string]any{
		"chunk_id": chunkID, "knowledge_id": knowledgeID,
		"knowledge_base_id": "field-research-kb", "content": body,
		"content_complete": true,
	}
	if len(locatorPayload) > 0 {
		chunk["source_locators"] = []any{map[string]any{"origin_metadata": locatorPayload[0]}}
	}
	data := map[string]any{
		"display_type":      "knowledge_chunks_list",
		"knowledge_id":      knowledgeID,
		"knowledge_base_id": "field-research-kb",
		"chunks":            []any{chunk},
	}
	engine.modelContext.ModelToolResultForTool(tool, &types.ToolResult{Success: true, Data: data})
	payload, err := json.Marshal(map[string]any{"success": true, "data": data})
	require.NoError(t, err)
	return chat.Message{Role: "tool", Name: tool, ToolCallID: callID, Content: string(payload)}
}

func TestRewriteCitationDirectoryUsesFullSuccessfulMessagesAfterEvidenceViewsWereClipped(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	const sourceCount = 9
	sourceMessages := make([]chat.Message, 0, sourceCount)
	allIDs := make([]string, 0, sourceCount)
	for i := 0; i < sourceCount; i++ {
		id := fmt.Sprintf("field-observation-%02d", i)
		allIDs = append(allIDs, id)
		tool := "read_document"
		if i%2 == 1 {
			tool = "search_knowledge"
		}
		sourceMessages = append(sourceMessages, rewriteDirectorySourceMessage(
			t, engine, tool, fmt.Sprintf("call-%02d", i), fmt.Sprintf("survey-%02d", i), id,
			fmt.Sprintf("Watershed station %02d reported stable daily flow. %s", i, strings.Repeat("measured-value ", 100)),
			fmt.Sprintf("origin-%02d/", i)+strings.Repeat("mapped-source-metadata-", 100),
		))
	}
	var draftBuilder strings.Builder
	for i, id := range allIDs {
		fmt.Fprintf(&draftBuilder, "Observation %02d is supported <kb doc=\"Survey %02d\" chunk_id=\"%s\" kb_id=\"field-research-kb\" />. ", i, i, id)
	}
	draft := draftBuilder.String()
	citationView := citedSourceEvidenceFromMessages(sourceMessages, draft, citationReviewEvidenceBudget, false)
	companionView := documentRetrievalCompanion(sourceMessages, documentRetrievalCompanionBudget)
	require.NotNil(t, companionView)
	citationViewBytes, err := json.Marshal(citationView)
	require.NoError(t, err)
	var citationViewCheck map[string]any
	require.NoError(t, json.Unmarshal(citationViewBytes, &citationViewCheck))
	require.Greater(t, int(citationViewCheck["omitted_citation_items"].(float64)), 0, "the real 12 KiB citation projection must omit some source items")
	companionBytes, err := json.Marshal(companionView)
	require.NoError(t, err)
	require.LessOrEqual(t, len(companionBytes), documentRetrievalCompanionBudget)
	var companionCheck map[string]any
	require.NoError(t, json.Unmarshal(companionBytes, &companionCheck))
	require.Greater(t, int(companionCheck["omitted_body_count"].(float64)), 0, "the real document companion must omit some long bodies")
	inputBytes, err := json.Marshal(map[string]any{
		"unverified_draft":             draft,
		"citation_source_evidence":     citationView,
		"document_retrieval_companion": companionView,
		"source_tool_results":          []any{"already bounded original payload view"},
	})
	require.NoError(t, err)
	messages := []chat.Message{{Role: "system", Content: "Review evidence bindings."}, {Role: "user", Content: string(inputBytes)}}
	original := append([]chat.Message(nil), messages...)
	originalInput := rewriteCitationReviewInput(t, messages)
	bound := engine.bindIndependentReviewCitations(messages, sourceMessages)
	require.Equal(t, original, messages, "binding must not mutate the original envelope")
	input := rewriteCitationReviewInput(t, bound)
	view, ok := input["source_citation_handles"].(map[string]any)
	require.True(t, ok)
	bindings, ok := view["bindings"].([]any)
	require.True(t, ok)
	require.Len(t, bindings, sourceCount)
	require.Equal(t, sourceCount, int(view["source_chunk_count"].(float64)))
	require.Equal(t, sourceCount, int(view["included_binding_count"].(float64)))
	require.Equal(t, 0, int(view["omitted_binding_count"].(float64)))
	require.True(t, view["projection_complete"].(bool), "this mapping view is complete for the full supplied successful observations")

	byID := map[string]map[string]any{}
	for _, raw := range bindings {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		byID[item["chunk_id"].(string)] = item
	}
	for i := range allIDs {
		id := allIDs[i]
		item, ok := byID[id]
		require.True(t, ok, "a real draft citation cannot be lost merely because a separate view was clipped")
		require.Equal(t, engine.modelContext.CitableChunkHandle(id), item["citation_handle"])
		require.Equal(t, sourceMessages[i].Name, item["source_tool"])
		require.Equal(t, "/data/chunks/0/content", item["json_pointer"])
		require.NotEmpty(t, item["evidence_id"])
	}
	reader := newReviewEvidenceReader(sourceMessages)
	for _, i := range []int{7, 8} {
		item := byID[allIDs[i]]
		arguments, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": item["evidence_id"], "json_pointer": item["json_pointer"], "max_chars": 6000})
		require.NoError(t, err)
		read := reader.read(string(arguments))
		require.Equal(t, "ok", read["status"])
		require.Contains(t, read["text"], fmt.Sprintf("Watershed station %02d reported stable daily flow.", i))
		require.True(t, read["view_complete"].(bool), "the late mapping opens the exact complete immutable text")
	}
	require.Equal(t, originalInput["source_tool_results"], input["source_tool_results"], "original tool-result projection is preserved")
	require.Equal(t, originalInput["citation_source_evidence"], input["citation_source_evidence"])
}

func TestRewriteCitationDirectoryPrioritizesDraftAndRejectsUnknownNavigationAndConflicts(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	sourceMessages := make([]chat.Message, 0, 96)
	ids := make([]string, 0, 96)
	for i := 0; i < 96; i++ {
		id := fmt.Sprintf("observation-%03d-%s", i, strings.Repeat("x", 96))
		ids = append(ids, id)
		sourceMessages = append(sourceMessages, rewriteDirectorySourceMessage(
			t, engine, "read_document", fmt.Sprintf("tool-%03d", i), fmt.Sprintf("record-%03d", i), id,
			fmt.Sprintf("Field observation %03d is within expected range.", i),
		))
	}
	conflictID := "same-id-different-source-content"
	sourceMessages = append(sourceMessages,
		rewriteDirectorySourceMessage(t, engine, "read_document", "conflict-one", "record-conflict-a", conflictID, "First observation body."),
		rewriteDirectorySourceMessage(t, engine, "search_knowledge", "conflict-two", "record-conflict-b", conflictID, "A different observation body."),
	)
	unknownID := "unfetched-field-note"
	navigationID := "historical-navigation-only"
	engine.modelContext.RegisterContextChunk(structuredNavigationFixture(navigationID))
	// Keep the valid cited item late in source order so the budget proves draft
	// mappings are prioritized over ordinary directory rows.
	validDraftID := ids[len(ids)-1]
	draft := fmt.Sprintf("Verified <kb doc=\"field note\" chunk_id=\"%s\" kb_id=\"record-095\" />. Also <kb doc=\"old note\" chunk_id=\"%s\" kb_id=\"old-kb\" /> <kb doc=\"missing\" chunk_id=\"%s\" kb_id=\"other\" /> <kb doc=\"ambiguous\" chunk_id=\"%s\" kb_id=\"other\" />.", validDraftID, navigationID, unknownID, conflictID)
	inputBytes, err := json.Marshal(map[string]any{"unverified_draft": draft, "source_citation_handles": map[string]any{"old": "clipped"}})
	require.NoError(t, err)
	messages := []chat.Message{{Role: "system", Content: "Review source bindings."}, {Role: "user", Content: string(inputBytes)}}
	bound := engine.bindIndependentReviewCitations(messages, sourceMessages)
	input := rewriteCitationReviewInput(t, bound)
	view, ok := input["source_citation_handles"].(map[string]any)
	require.True(t, ok)
	encodedView, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encodedView), rewriteCitationHandlesBudget, "the serialized binding envelope obeys its hard byte cap")
	bindings, ok := view["bindings"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, bindings)
	first := bindings[0].(map[string]any)
	require.Equal(t, validDraftID, first["chunk_id"], "late draft-cited sources are ordered before non-draft directory entries")
	require.Equal(t, engine.modelContext.CitableChunkHandle(validDraftID), first["citation_handle"])
	require.Equal(t, 3, int(view["unavailable_draft_citation_count"].(float64)), "unknown, navigation-only, and conflicting draft citations are all unavailable")
	require.Equal(t, 3, int(view["unavailable_binding_count"].(float64)), "none of the three invalid draft IDs enters the directory")
	require.False(t, view["projection_complete"].(bool))
	require.NotContains(t, string(encodedView), navigationID)
	require.NotContains(t, string(encodedView), unknownID)
	require.NotContains(t, string(encodedView), conflictID)
	require.Empty(t, engine.modelContext.CitableChunkHandle(navigationID))
}

func structuredNavigationFixture(chunkID string) modelcontext.ChunkReference {
	return modelcontext.ChunkReference{ChunkID: chunkID, KnowledgeID: "old-field-log", KnowledgeBaseID: "archive-kb", DocumentTitle: "Historical field log"}
}
