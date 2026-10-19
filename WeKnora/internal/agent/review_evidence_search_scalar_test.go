package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestReviewEvidenceScalarSearchCanExposeHiddenNumberToReceipt(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "numeric-source", map[string]any{
		"cells": []any{map[string]any{"cell": "B2824", "value": json.Number("54.96")}},
	})})
	record := reader.records[0]
	response := reader.search("54.96", record.ID, "", 24)
	require.Equal(t, "ok", response["status"])
	require.Equal(t, true, response["scan_complete"])
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 1)
	require.Equal(t, "/data/cells/0/value", matches[0].JSONPointer)
	require.Contains(t, matches[0].ContextText, "54.96")
	require.Contains(t, response["notice"], "numbers retain their original JSON spelling")

	args, err := json.Marshal(map[string]any{"operation": "search", "query": "54.96", "evidence_id": record.ID})
	require.NoError(t, err)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "scalar-search", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "scalar-search", Content: string(encoded)},
	}
	require.NoError(t, reviewWitnessVisible(map[string]any{}, reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: matches[0].JSONPointer, Quote: "54.96", Kind: "source",
	}), "the exact successful scalar search observation must satisfy receipt visibility")
}

func TestReviewEvidenceScalarSearchPreservesNumberLexemes(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "lexical-numbers", map[string]any{
		"scientific": json.Number("1e+09"),
		"large":      json.Number("900719925474099312345678901"),
	})})
	record := reader.records[0]

	for _, item := range []struct{ query, pointer string }{
		{"1e+09", "/data/scientific"},
		{"900719925474099312345678901", "/data/large"},
	} {
		response := reader.search(item.query, record.ID, "", 24)
		require.Equal(t, "ok", response["status"])
		matches := reviewSearchMatches(t, response)
		require.Len(t, matches, 1)
		require.Equal(t, item.pointer, matches[0].JSONPointer)
		require.Contains(t, matches[0].ContextText, item.query)
	}
	response := reader.search("1000000000", record.ID, "", 24)
	require.Equal(t, 0, response["match_count"], "scientific notation is searched as its exact original JSON spelling")
}

func TestReviewEvidenceScalarSearchIncludesBooleanAndNullJSONText(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "scalar-types", map[string]any{
		"enabled": true,
		"missing": nil,
	})})
	record := reader.records[0]
	for _, item := range []struct{ query, pointer string }{{"true", "/data/enabled"}, {"null", "/data/missing"}} {
		response := reader.search(item.query, record.ID, "", 24)
		require.Equal(t, "ok", response["status"])
		matches := reviewSearchMatches(t, response)
		require.Len(t, matches, 1)
		require.Equal(t, item.pointer, matches[0].JSONPointer)
		require.Contains(t, matches[0].ContextText, item.query)
	}
}

func TestReviewEvidenceScalarSearchRemainsBoundToEligibleSuccessfulIdentity(t *testing.T) {
	failedContent, err := json.Marshal(map[string]any{"success": false, "data": map[string]any{"value": json.Number("77.7")}})
	require.NoError(t, err)
	reader := newReviewEvidenceReader([]chat.Message{
		{Role: "tool", Name: "read_document", ToolCallID: "failed-scalar", Content: string(failedContent)},
		{Role: "tool", Name: "plan_task", ToolCallID: "planner-scalar", Content: `{"success":true,"data":{"value":77.7}}`},
		{Role: "tool", Name: "describe_tools", ToolCallID: "directory-scalar", Content: `{"success":true,"data":{"value":77.7}}`},
		successfulSearchResult(t, "other-source", map[string]any{"value": json.Number("77.7")}),
		successfulSearchResult(t, "target-source", map[string]any{"value": json.Number("12.3")}),
	})

	for _, record := range reader.records[:3] {
		response := reader.search("77.7", record.ID, "", 24)
		require.NotEqual(t, "ok", response["status"], "failed, planner, and tool-directory results cannot be searched")
	}
	target := reader.records[4]
	response := reader.search("77.7", target.ID, "", 24)
	require.Equal(t, "ok", response["status"])
	require.Equal(t, 0, response["match_count"], "a matching scalar under a different immutable evidence ID cannot cross-bind")
}

func TestReviewEvidenceScalarSearchBudgetExhaustionRemainsPartial(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "scalar-budget", map[string]any{
		"a_text":  strings.Repeat("x", reviewEvidenceSearchRuneBudget+1),
		"z_value": json.Number("42"),
	})})
	response := reader.search("42", reader.records[0].ID, "", 24)
	require.Equal(t, "partial", response["status"])
	require.Equal(t, false, response["scan_complete"])
	require.Equal(t, 0, response["match_count"], "a scalar beyond the exhausted text budget is not inspected")
	require.Greater(t, response["checked_runes"], 0)
}

func TestReviewEvidenceScalarSearchDoesNotChangeTextLeafBehavior(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "text-still-literal", map[string]any{
		"content": "raw text contains 54.96 percent",
	})})
	response := reader.search("54.96", reader.records[0].ID, "", 24)
	require.Equal(t, "ok", response["status"])
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 1)
	require.Equal(t, "/data/content", matches[0].JSONPointer)
	require.Contains(t, matches[0].ContextText, "raw text contains 54.96 percent")
}
