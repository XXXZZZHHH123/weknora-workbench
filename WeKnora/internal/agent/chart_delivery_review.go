package agent

import (
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const chartDeliveryReviewBudget = 12 * 1024

// Project the real render result independently of answer citations. The PNG
// itself cannot expose which source cells/columns were plotted. Every omitted
// value keeps its immutable raw-result handle for the existing reader.
func chartDeliveryReviewEvidence(state *types.AgentState, messages []chat.Message) map[string]any {
	if state == nil {
		return nil
	}
	ids := map[string]string{}
	for _, message := range messages {
		var payload map[string]json.RawMessage
		if message.Name != renderChartToolName || json.Unmarshal([]byte(message.Content), &payload) != nil || payload["data"] == nil {
			continue
		}
		ids[message.ToolCallID] = reviewEvidenceID(message)
	}
	entries := []any{}
	complete := true
	omittedResults := 0
	budget := chartDeliveryReviewBudget - 1024
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if !chartDeliveryExecution(call) || call.Result == nil || ids[call.ID] == "" {
				continue
			}
			if budget < 512 {
				complete = false
				omittedResults++
				continue
			}
			callID := call.ID
			if len(callID) > 256 {
				callID = "sha256:" + candidateTextDigest(callID)
			}
			entry := map[string]any{"tool_call_id": callID, "evidence_id": ids[call.ID], "json_pointer": "/data", "success": call.Result.Success}
			omitted := []string{}
			data := map[string]any{}
			entry["data"] = data
			if call.Result.Error != "" {
				entry["error"] = call.Result.Error
			}
			if call.Result.Output != "" {
				entry["output"] = call.Result.Output
			}
			for _, key := range []string{"kind", "status", "artifact", "delivery_mode", "source_id", "chart_spec", "selected_columns", "source_mapping", "row_count", "source_truncated", "provenance", "semantic_validation", "selected_rows"} {
				value, ok := call.Result.Data[key]
				if !ok {
					continue
				}
				data[key] = value
				if sourceReviewProjectionSize(entry) > budget {
					delete(data, key)
					omitted = append(omitted, "/data/"+strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"))
				}
			}
			entry["projection_complete"] = len(omitted) == 0
			if len(omitted) > 0 {
				entry["omitted_pointers"] = omitted
				complete = false
			}
			// Keep even a capacity-omitted render's identity so the reader can
			// restore that result instead of treating it as never generated.
			if sourceReviewProjectionSize(entry) > budget {
				entry = map[string]any{"tool_call_id": callID, "evidence_id": ids[call.ID], "json_pointer": "/data", "success": call.Result.Success, "projection_complete": false, "omitted_pointers": []string{"/data", "/error", "/output"}}
				complete = false
			}
			if sourceReviewProjectionSize(entry) > budget {
				omittedResults++
				complete = false
				continue
			}
			entries = append(entries, entry)
			budget -= sourceReviewProjectionSize(entry)
		}
	}
	if len(entries) == 0 && omittedResults == 0 {
		return nil
	}
	return map[string]any{"entries": entries, "projection_complete": complete, "omitted_result_count": omittedResults, "notice": "Actual current-turn render attempts, not plans. Successful saving proves an artifact exists, not that its plotted variables answer the task. selected_rows are the complete actual plotted rows when projection_complete=true; omitted pointers need the immutable evidence reader. Use the reader's list operation for capacity-omitted render results. Chart labels and title alone do not establish the meaning of selected cells."}
}
