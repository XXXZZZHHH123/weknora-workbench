package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// This tool returns the current synthetic source. Replacing it between the
// suspended run and its replica models an original-file version change; no
// history result is rerun just to obtain a citation handle.
type resumeCitationReadTool struct {
	agenttools.BaseTool
	result *types.ToolResult
	calls  int
}

func (tool *resumeCitationReadTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return tool.result, nil
}

func installResumeCitationRead(t *testing.T, fixture *suspendFixture, data map[string]any) *resumeCitationReadTool {
	t.Helper()
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	read := &resumeCitationReadTool{BaseTool: agenttools.NewBaseTool("structured_read_pdf_table", "", json.RawMessage(`{"type":"object"}`)),
		result: &types.ToolResult{Success: true, Output: string(encoded), Data: data}}
	fixture.engine.config.EvidenceReferencesEnabled = true
	fixture.engine.config.WorkbenchDefault = true
	fixture.engine.config.TaskPlanningEnabled = true
	// Registry names are first-wins. Rebuild this isolated fixture rather than
	// weakening collision protection to replace its counting-tool stand-in.
	fixture.engine.toolRegistry = agenttools.NewToolRegistry()
	fixture.engine.toolRegistry.RegisterTool(read)
	fixture.engine.toolRegistry.RegisterTool(fixture.blocked)
	fixture.engine.toolRegistry.RegisterTool(fixture.after)
	return read
}

func revisedResumeCitationSource(t *testing.T, original map[string]any) map[string]any {
	t.Helper()
	oldSHA := original["source"].(map[string]any)["sha256"].(string)
	const newSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	var replace func(any) any
	replace = func(value any) any {
		switch typed := value.(type) {
		case string:
			if typed == oldSHA {
				return newSHA
			}
			return typed
		case []any:
			copy := make([]any, len(typed))
			for i, item := range typed {
				copy[i] = replace(item)
			}
			return copy
		case map[string]any:
			copy := make(map[string]any, len(typed))
			for key, item := range typed {
				copy[key] = replace(item)
			}
			return copy
		default:
			return typed
		}
	}
	data := replace(original).(map[string]any)
	contract := data["result_contract"].(map[string]any)
	delete(contract, "result_hash")
	payload := map[string]any{}
	for _, key := range contract["hash_input_keys"].([]any) {
		payload[key.(string)] = data[key.(string)]
	}
	hash, err := structuredresult.TypedSHA256(map[string]any{"payload": payload, "contract": contract})
	require.NoError(t, err)
	contract["result_hash"] = hash
	validated, err := structuredresult.ValidatePayload(data)
	require.NoError(t, err)
	require.Equal(t, newSHA, validated.SourceSnapshot.SourceSHA256)
	return data
}

func TestStructuredCitationResumeOnlyRegistersCallsExecutedAfterApproval(t *testing.T) {
	for _, reread := range []bool{false, true} {
		name := "history_only"
		if reread {
			name = "new_current_read"
		}
		t.Run(name, func(t *testing.T) {
			_, rawState := rawCitationFixture(t)
			oldData := rawState.RoundSteps[0].ToolCalls[0].Result.Data
			first := newSuspendFixture(t, "structured_read_pdf_table", false)
			oldRead := installResumeCitationRead(t, first, oldData)
			if reread {
				first.engine.chatModel.(*mockChat).responses[0].chunks[0].ToolCalls[2].Function.Name = "structured_read_pdf_table"
			}
			first.engine.EnableSuspension(nil)
			paused := first.run(t)
			require.NotNil(t, paused.Suspended)
			require.NoError(t, first.blocked.err)
			require.Equal(t, 1, oldRead.calls)
			saved, err := DecodeRunSnapshot(first.blocked.snapshot)
			require.NoError(t, err)
			require.Len(t, saved.PendingRound.Step.ToolCalls, 1)
			require.Equal(t, "call-a", saved.PendingRound.Step.ToolCalls[0].ID)

			// A fresh replica sees a revised actual source. The saved prefix must
			// remain a navigation observation even when later calls finish.
			current := revisedResumeCitationSource(t, oldData)
			resumed := newSuspendFixture(t, "structured_read_pdf_table", false)
			freshRead := installResumeCitationRead(t, resumed, current)
			model := resumed.engine.chatModel.(*mockChat)
			model.responses = []mockResponse{
				{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "Finished.", Done: true, FinishReason: "stop"}}},
				sourceAcceptedVerdict(),
			}
			resumed.engine.EnableSuspension(nil)
			state, err := resumed.engine.Resume(t.Context(), "sess-1", "msg-1", nil, first.blocked.snapshot, answeredPrompt("call-b"))
			require.NoError(t, err)
			require.True(t, state.IsComplete)
			require.NotNil(t, resumed.blocked.answered)
			if !reread {
				require.Zero(t, freshRead.calls)
				require.Empty(t, resumed.engine.turnReview.StructuredCitations, "the old prefix did not execute after approval")
			} else {
				require.Equal(t, 1, freshRead.calls)
				require.NotEmpty(t, resumed.engine.turnReview.StructuredCitations)
			}
			for id, observation := range resumed.engine.turnReview.StructuredCitations {
				require.Equal(t, "call-c", observation.CallID, "a completed pending-round prefix must not be promoted by the resumed suffix")
				require.Equal(t, strings.Repeat("e", 64), observation.Reference.Sources[0].SourceSHA256)
				_, current := resumed.engine.currentStructuredObservation(id, state)
				require.True(t, current)
			}
			require.NotEmpty(t, model.calls)
			sawOld, sawFresh := false, false
			for _, message := range model.calls[0] {
				if message.Role != "tool" {
					continue
				}
				if message.ToolCallID == "call-a" {
					sawOld = true
					require.NotContains(t, message.Content, "<structured_evidence_citations>")
				}
				if message.ToolCallID == "call-c" && reread {
					sawFresh = true
					require.Contains(t, message.Content, "<structured_evidence_citations>")
				}
			}
			require.True(t, sawOld, "the source history remains visible without current citation authority")
			require.Equal(t, reread, sawFresh)
		})
	}
}

func TestStructuredCitationSnapshotPreservesLargeNumbersAndRejectsTrailingObjects(t *testing.T) {
	snapshot := RunSnapshot{Version: RunSnapshotVersion, Query: "Resume synthetic observation", PendingRound: PendingRound{Step: types.AgentStep{ToolCalls: []types.ToolCall{{ID: "old-read", Name: "structured_read_pdf_table",
		Result: &types.ToolResult{Success: true, Data: map[string]any{"native_integer": json.Number("9007199254740993")}}}}}}}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	decoded, err := DecodeRunSnapshot(raw)
	require.NoError(t, err)
	value := decoded.PendingRound.Step.ToolCalls[0].Result.Data["native_integer"]
	require.IsType(t, json.Number(""), value)
	require.Equal(t, "9007199254740993", value.(json.Number).String())
	_, err = DecodeRunSnapshot(append(bytes.Clone(raw), []byte(` {"version":1}`)...))
	require.Error(t, err)
}
