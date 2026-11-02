package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func pdfReadReviewFixture(t *testing.T) types.ToolCall {
	t.Helper()
	data := map[string]any{"kind": "structured_source_inspection", "source_kind": "pdf_table", "status": "ok", "table_id": "pdft-observed", "review_status": "reviewed", "offset": 0, "next_offset": 2, "total_row_count": 9, "returned_row_count": 2, "output_truncated": true,
		"source": map[string]any{"knowledge_id": "actual-document", "knowledge_base_id": "authorized-space", "sha256": "actual-immutable-hash"}, "fragments": []any{map[string]any{"page": 4, "caption": "Observed source table"}},
		"rows": []any{map[string]any{"values": map[string]any{"label": "Header", "measurement": "Amount"}}, map[string]any{"values": map[string]any{"label": "Aggregate", "measurement": "27.50"}, "source_cells": map[string]any{"measurement": []any{map[string]any{"page": 4, "row": 1, "column": 1, "text": "27.50"}}}}}}
	call, _ := structuredQueryFixture(t, "structured_read_pdf_table", "early-actual-read", map[string]any{"knowledge_base_id": "authorized-space", "table_id": "pdft-observed"}, data, true)
	return call
}

func TestPDFReadCompanionSurvivesGenericRecentResultLimit(t *testing.T) {
	for _, route := range []string{"typed", "gateway", "resolved_target"} {
		t.Run(route, func(t *testing.T) {
			call := pdfReadReviewFixture(t)
			switch route {
			case "gateway":
				call.Name = "structured_data"
				call.Args = map[string]any{"operation": "read_pdf_table", "arguments": call.Args}
			case "resolved_target":
				call.Target = &types.ToolCallTarget{Name: call.Name, Args: call.Args}
				call.Name = "call_mcp_tool"
				call.Args = map[string]any{"tool_ref": "actual-resolved-reference"}
			}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			for i := 0; i < 8; i++ {
				later, _ := structuredQueryFixture(t, "read_document", string(rune('a'+i)), nil, map[string]any{"notice": "index only"}, true)
				state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, later)
			}
			engine := sourceConsistencyEngine(t, &mockChat{})
			source := engine.sourceConsistencyMessages(state, nil)
			generic, _ := boundedReviewEvidence(source, sourceReviewToolResultsBudget)
			for _, item := range generic {
				require.NotEqual(t, call.ID, item.ToolCallID, "the generic view must really omit this early read")
			}
			view := structuredQueryRowReviewEvidence(state, source, structuredQueryRowReviewBudget)
			entries, size := decodeStructuredQueryCompanion(t, view)
			require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
			require.Len(t, entries, 1)
			entry := entries[0]
			require.Equal(t, call.ID, entry.ToolCallID)
			require.Equal(t, "pdft-observed", entry.Metadata["table_id"])
			require.Equal(t, 2, entry.SourceRowCount)
			require.Equal(t, 2, entry.RetainedRowCount)
			require.True(t, entry.ProjectionComplete)
			require.Equal(t, true, entry.SourceResultTruncated, "returned-row completeness is not original-source completeness")
			require.Contains(t, entry.Metadata, "source")
			require.Contains(t, entry.Metadata, "fragments")
			reader := newReviewEvidenceReader(source)
			witness := reviewSourceWitness{EvidenceID: entry.EvidenceID, JSONPointer: "/data/rows/1/values/measurement", Quote: "27.50", Kind: "source"}
			require.NoError(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, witness))
		})
	}
}

func TestPDFReadCompanionRequiresCanonicalSuccessfulRead(t *testing.T) {
	mutations := map[string]func(*types.ToolCall){"failed": func(c *types.ToolCall) { c.Result.Success = false }, "error_status": func(c *types.ToolCall) { c.Result.Data["status"] = "error" }, "directory": func(c *types.ToolCall) { c.Result.Data["kind"] = "structured_pdf_tables" }, "other_source": func(c *types.ToolCall) { c.Result.Data["source_kind"] = "excel" }, "wrong_tool": func(c *types.ToolCall) { c.Name = "read_document" }, "wrong_gateway": func(c *types.ToolCall) {
		c.Name = "structured_data"
		c.Args = map[string]any{"operation": "list_pdf_tables"}
	}, "display_only": func(c *types.ToolCall) { c.Result.Data = nil }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			call := pdfReadReviewFixture(t)
			mutate(&call)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			engine := sourceConsistencyEngine(t, &mockChat{})
			require.Nil(t, structuredQueryRowReviewEvidence(state, engine.sourceConsistencyMessages(state, nil), structuredQueryRowReviewBudget))
		})
	}
}

func TestPDFReadCompanionOmittedRowsRetainRecoverableIdentity(t *testing.T) {
	call := pdfReadReviewFixture(t)
	call.Result.Data["rows"].([]any)[0].(map[string]any)["values"].(map[string]any)["measurement"] = strings.Repeat("wide-source-text", 3000)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	source := engine.sourceConsistencyMessages(state, nil)
	view := structuredQueryRowReviewEvidence(state, source, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.False(t, entry.ProjectionComplete)
	require.Zero(t, entry.RetainedRowCount)
	require.NotEmpty(t, entry.OmittedRowRanges)
	require.Equal(t, "/data/rows", entry.OmittedRowRanges[0].JSONPointer)
	reader := newReviewEvidenceReader(source)
	record := reader.records[reader.byID[entry.EvidenceID]]
	value, err := reviewPointerValue(record.Value, "/data/rows/1/values/measurement")
	require.NoError(t, err)
	require.Equal(t, "27.50", value)
	require.Error(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, reviewSourceWitness{EvidenceID: entry.EvidenceID, JSONPointer: "/data/rows/1/values/measurement", Quote: "27.50", Kind: "source"}), "hidden values still require reading")
}

// Optional read-only projection replay; business fixtures stay outside the repo.
func TestPDFReadCompanionPrivateReplay(t *testing.T) {
	path := os.Getenv("PDF_READ_REVIEW_REPLAY")
	if path == "" {
		t.Skip("set PDF_READ_REVIEW_REPLAY to a private saved result")
	}
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	var saved struct {
		Answer struct {
			Steps []types.AgentStep `json:"agent_steps"`
		} `json:"answer"`
	}
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&saved))
	state := &types.AgentState{RoundSteps: saved.Answer.Steps}
	engine := sourceConsistencyEngine(t, &mockChat{})
	source := engine.sourceConsistencyMessages(state, nil)
	view := structuredQueryRowReviewEvidence(state, source, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	reader := newReviewEvidenceReader(source)
	reads := 0
	restored := []map[string]any{}
	for _, entry := range entries {
		if entry.Metadata["kind"] != "structured_source_inspection" {
			continue
		}
		reads++
		require.NotEmpty(t, entry.EvidenceID)
		require.NotEmpty(t, entry.Metadata["table_id"])
		require.Contains(t, entry.Metadata, "source")
		require.Contains(t, entry.Metadata, "fragments")
		record := reader.records[reader.byID[entry.EvidenceID]]
		rows, err := reviewPointerValue(record.Value, entry.SourceJSONPointer)
		require.NoError(t, err)
		require.Len(t, rows, entry.SourceRowCount)
		originalSource, err := reviewPointerValue(record.Value, "/data/source")
		require.NoError(t, err)
		require.True(t, sameStructuredJSONValue(originalSource, entry.Metadata["source"]), "the original source identity must not be crowded out by added read observations")
		if len(entry.OmittedRowRanges) > 0 {
			pointer := fmt.Sprintf("/data/rows/%d/values", entry.OmittedRowRanges[0].StartIndex)
			args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": entry.EvidenceID, "json_pointer": pointer})
			require.NoError(t, err)
			response := reader.read(string(args))
			require.Equal(t, "ok", response["status"])
			originalValues, err := reviewPointerValue(record.Value, pointer)
			require.NoError(t, err)
			require.True(t, sameStructuredJSONValue(originalValues, response["value"]), "omitted source values must remain exactly recoverable")
			restored = append(restored, response)
		}
		t.Logf("read=%s rows=%d/%d source_truncated=%v omitted=%v metadata=%v", entry.ToolCallID, entry.RetainedRowCount, entry.SourceRowCount, entry.SourceResultTruncated, entry.OmittedRowRanges, entry.MetadataOmittedPointers)
	}
	require.Positive(t, reads)
	t.Logf("canonical_saved_structured_results=%d pdf_reads=%d companion_bytes=%d unchanged_budget=%d", len(entries), reads, size, structuredQueryRowReviewBudget)
	if out := os.Getenv("PDF_READ_REVIEW_REPLAY_OUTPUT"); out != "" {
		encoded, err := json.MarshalIndent(view, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, encoded, 0600))
		encoded, err = json.MarshalIndent(restored, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out+".readback.json", encoded, 0600))
	}
}
