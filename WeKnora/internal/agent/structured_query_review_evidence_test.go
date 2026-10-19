package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func structuredQueryFixture(t *testing.T, name, id string, args map[string]any, data map[string]any, success bool) (types.ToolCall, chat.Message) {
	t.Helper()
	dataJSON, err := json.Marshal(data)
	require.NoError(t, err)
	messagePayload := map[string]any{"success": success, "data": data}
	messageJSON, err := json.Marshal(messagePayload)
	require.NoError(t, err)
	call := types.ToolCall{Name: name, ID: id, Args: args, Result: &types.ToolResult{Success: success, Data: data, Output: string(dataJSON)}}
	message := chat.Message{Role: "tool", Name: name, ToolCallID: id, Content: string(messageJSON)}
	return call, message
}

func structuredQueryRowsFixture(count int) []any {
	rows := make([]any, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, map[string]any{
			"date": fmt.Sprintf("2020-%02d-01", i%12+1), "date_cell": fmt.Sprintf("A%d", i+2),
			"value": float64(i) + 0.25, "value_status": "valid", "formula": nil,
			"source_cell": fmt.Sprintf("K%d", i+2), "raw_value": fmt.Sprintf("%d.25", i),
		})
	}
	return rows
}

func decodeStructuredQueryCompanion(t *testing.T, value map[string]any) ([]structuredQueryRowCompanionEntry, int) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded struct {
		Entries []structuredQueryRowCompanionEntry `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded.Entries, len(encoded)
}

func TestStructuredQueryRowCompanionRetainsMoreThanOneHundredRows(t *testing.T) {
	rows := structuredQueryRowsFixture(106)
	data := map[string]any{
		"status": "ok", "dataset_id": "dataset-a", "series": map[string]any{"series_id": "series-a", "unit": "units", "sheet": "Research A"},
		"filters":      map[string]any{"start_date": "2014-11-01", "end_date": "2023-08-31"},
		"record_count": 154, "valid_count": 100, "formula_count": 54, "missing_count": 0,
		"truncated": false, "rows": rows,
	}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-a", nil, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{message}, structuredQueryRowReviewBudget)
	require.NotNil(t, view)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.True(t, view["projection_complete"].(bool))
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, 0, entry.EntryIndex)
	require.Equal(t, reviewEvidenceID(message), entry.EvidenceID)
	require.Equal(t, "/data/rows", entry.SourceJSONPointer)
	require.Equal(t, 106, entry.SourceRowCount)
	require.Equal(t, 106, entry.RetainedRowCount)
	require.True(t, entry.ProjectionComplete)
	require.Equal(t, false, entry.SourceResultTruncated)
	require.True(t, entry.SourceTruncationKnown)
	require.Equal(t, "positional", entry.RowEncoding)
	require.Equal(t, []string{"date", "date_cell", "raw_value", "source_cell", "value"}, entry.Columns)
	require.Equal(t, map[string]any{"formula": nil, "value_status": "valid"}, entry.ConstantFields)
	packed := entry.Rows.([]any)
	require.Len(t, packed, len(rows))
	require.Equal(t, []any{"2020-01-01", "A2", "0.25", "K2", 0.25}, packed[0])
	require.Equal(t, []any{"2020-10-01", "A107", "105.25", "K107", 105.25}, packed[105])
	require.Equal(t, float64(154), entry.Metadata["record_count"], "metadata counts remain distinct from row-window length")
}

func TestStructuredQueryRowCompanionUsesActualSourceConsistencyAndReviewReaderHandle(t *testing.T) {
	rows := structuredQueryRowsFixture(106)
	data := map[string]any{
		"status": "ok", "dataset_id": "dataset-live", "series": map[string]any{"series_id": "series-live", "unit": "units"},
		"filters":      map[string]any{"start_date": "2014-11-01", "end_date": "2023-08-31"},
		"record_count": 154, "valid_count": 100, "formula_count": 54, "truncated": false, "rows": rows,
	}
	call, _ := structuredQueryFixture(t, "structured_query_series", "query-live", nil, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	sourceMessages := engine.sourceConsistencyMessages(state, nil)
	require.Len(t, sourceMessages, 2, "runtime access and exact result payload remain distinct")
	var payloadMessage chat.Message
	for _, message := range sourceMessages {
		if structuredQueryMessageData(message.Content) != nil {
			payloadMessage = message
		}
	}
	require.NotEmpty(t, payloadMessage.Content)
	view := structuredQueryRowReviewEvidence(state, sourceMessages, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.Len(t, entries, 1)
	require.Equal(t, reviewEvidenceID(payloadMessage), entries[0].EvidenceID,
		"companion identity must address the exact immutable payload handled by read_review_evidence")
	require.True(t, entries[0].ProjectionComplete)
	require.Len(t, entries[0].Rows.([]any), 106)

	reader := newReviewEvidenceReader(sourceMessages)
	args, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": entries[0].EvidenceID,
		"json_pointer": "/data/rows/105/value", "offset": 0, "max_chars": 100,
	})
	require.NoError(t, err)
	read := reader.read(string(args))
	require.Equal(t, "ok", read["status"])
	require.Equal(t, "105.25", fmt.Sprint(read["value"]))

	// The result JSON is already the exact Data rendering, so projection does
	// not retain an unnecessary duplicate Output string.
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(payloadMessage.Content), &payload))
	require.NotContains(t, payload, "output")
}

func TestSourceCriticCanReadRowsOmittedByReviewBudgets(t *testing.T) {
	rows := make([]any, 0, 100)
	firstDate := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		rows = append(rows, map[string]any{
			"date":  firstDate.AddDate(0, 0, i).Format("2006-01-02"),
			"value": strings.Repeat("x", 180) + fmt.Sprintf("-%03d", i),
		})
	}
	data := map[string]any{
		"status": "ok", "dataset_id": "generic-large-result", "record_count": len(rows),
		"actual_first_date": firstDate.Format("2006-01-02"), "actual_last_date": firstDate.AddDate(0, 0, len(rows)-1).Format("2006-01-02"),
		"truncated": false, "rows": rows,
	}
	call, _ := structuredQueryFixture(t, "structured_query_series", "query-companion-clipped", nil, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	e := sourceConsistencyEngine(t, &mockChat{})
	require.True(t, e.needsSourceConsistencyCheck(state), "the fixture must pass the real current-evidence review guard")
	sourceMessages := e.sourceConsistencyMessages(state, nil)
	_, sourceScope := boundedReviewEvidence(sourceMessages, sourceReviewToolResultsBudget)
	require.False(t, sourceScope.ViewComplete, "the general projection now shares its budget with source bodies")

	companion := structuredQueryRowReviewEvidence(state, sourceMessages, structuredQueryRowReviewBudget)
	require.NotNil(t, companion)
	require.False(t, companion["projection_complete"].(bool), "the independent row companion budget must omit a suffix")
	entries, _ := decodeStructuredQueryCompanion(t, companion)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Less(t, entry.RetainedRowCount, len(rows))
	require.Equal(t, len(rows), entry.SourceRowCount)
	rowIndex := entry.RetainedRowCount
	omittedValue := rows[rowIndex].(map[string]any)["value"].(string)

	model := &mockChat{responses: []mockResponse{
		reviewEvidenceToolResponse(reviewEvidenceToolCall(t, "read-companion-omission", entry.EvidenceID,
			fmt.Sprintf("/data/rows/%d/value", rowIndex), 0, 256)),
		sourceAcceptedVerdict(),
	}}
	e.chatModel = model
	question := "Review the complete returned series evidence."
	repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), question, "The returned series is complete.", state, 0,
		[]chat.Message{{Role: "user", Content: question}})
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, model.calls, 2, "the critic must read a row omitted from its bounded views")
	require.Equal(t, reviewEvidenceToolName, model.opts[0].Tools[0].Function.Name)
	require.Contains(t, model.calls[0][1].Content, "review_evidence_access")
	var reviewInput map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &reviewInput))
	require.False(t, reviewInput["evidence_scope"].(map[string]any)["view_complete"].(bool))
	require.False(t, reviewInput["structured_query_row_companion"].(map[string]any)["projection_complete"].(bool))
	require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, omittedValue,
		"the original exact evidence pointer must restore the first omitted row")
}

func TestStructuredQueryRowCompanionDistinguishesSourceTruncationFromProjection(t *testing.T) {
	data := map[string]any{"status": "ok", "dataset_id": "dataset-b", "truncated": true, "rows": structuredQueryRowsFixture(2)}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-truncated", nil, data, true)
	view := structuredQueryRowReviewEvidence(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.True(t, entries[0].ProjectionComplete)
	require.True(t, entries[0].SourceResultTruncated.(bool))
	require.Empty(t, entries[0].OmittedRowRanges)
}

func TestStructuredQueryRowCompanionPreservesHeterogeneousRowsAndNulls(t *testing.T) {
	rows := []any{
		map[string]any{"cell": "B2", "value": nil, "extra": "kept"},
		map[string]any{"cell": "B3", "value": float64(2)}, // missing `extra` is distinct from an explicit null.
	}
	data := map[string]any{"status": "ok", "dataset_id": "dataset-c", "truncated": false, "rows": rows}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-heterogeneous", nil, data, true)
	view := structuredQueryRowReviewEvidence(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	entry := entries[0]
	require.Equal(t, "objects", entry.RowEncoding)
	require.Empty(t, entry.Columns)
	serialized, err := json.Marshal(entry.Rows)
	require.NoError(t, err)
	var actual []map[string]any
	require.NoError(t, json.Unmarshal(serialized, &actual))
	require.Equal(t, "kept", actual[0]["extra"])
	require.Contains(t, actual[0], "value")
	require.Nil(t, actual[0]["value"])
	require.NotContains(t, actual[1], "extra")
	require.Equal(t, float64(2), actual[1]["value"])
}

func TestStructuredQueryRowCompanionCompressesConstantsAndReconstructs154Rows(t *testing.T) {
	rows := make([]any, 0, 154)
	for i := 0; i < 154; i++ {
		rows = append(rows, map[string]any{
			"category_cell": nil, "date": fmt.Sprintf("2010-%02d-30", i%12+1), "date_cell": fmt.Sprintf("A%d", i+3),
			"formula": nil, "metric_cell": nil, "period": nil, "period_cell": fmt.Sprintf("P%d", i+1),
			"raw_value": fmt.Sprintf("%.2f", 30+float64(i)/10), "source_cell": fmt.Sprintf("K%d", i+3),
			"value": 30 + float64(i)/10, "value_status": "valid",
		})
	}
	data := map[string]any{"status": "ok", "dataset_id": "dataset-154", "record_count": 154, "valid_count": 154, "truncated": false, "rows": rows}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-154", nil, data, true)
	view := structuredQueryRowReviewEvidence(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.True(t, view["projection_complete"].(bool))
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, 154, entry.SourceRowCount)
	require.Equal(t, 154, entry.RetainedRowCount)
	require.Equal(t, "positional", entry.RowEncoding)
	require.Equal(t, map[string]any{"category_cell": nil, "formula": nil, "metric_cell": nil, "period": nil, "value_status": "valid"}, entry.ConstantFields)
	require.Equal(t, []string{"date", "date_cell", "period_cell", "raw_value", "source_cell", "value"}, entry.Columns)
	packedRows := entry.Rows.([]any)
	require.Len(t, packedRows, len(rows))
	for i, raw := range packedRows {
		values := raw.([]any)
		reconstructed := make(map[string]any, len(entry.ConstantFields)+len(entry.Columns))
		for key, value := range entry.ConstantFields {
			reconstructed[key] = value
		}
		for columnIndex, column := range entry.Columns {
			reconstructed[column] = values[columnIndex]
		}
		gotJSON, err := json.Marshal(reconstructed)
		require.NoError(t, err)
		expectedJSON, err := json.Marshal(rows[i])
		require.NoError(t, err)
		require.JSONEq(t, string(expectedJSON), string(gotJSON))
	}
}

func TestStructuredQueryRowCompanionPreservesRowCountWhenAllFieldsAreConstant(t *testing.T) {
	rows := []any{
		map[string]any{"cell": "A2", "value": nil, "unit": "unit"},
		map[string]any{"cell": "A2", "value": nil, "unit": "unit"},
		map[string]any{"cell": "A2", "value": nil, "unit": "unit"},
	}
	data := map[string]any{"status": "ok", "dataset_id": "constant", "truncated": false, "rows": rows}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-constant", nil, data, true)
	view := structuredQueryRowReviewEvidence(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	entry := entries[0]
	require.Empty(t, entry.Columns)
	require.Equal(t, map[string]any{"cell": "A2", "unit": "unit", "value": nil}, entry.ConstantFields)
	require.Len(t, entry.Rows.([]any), 3)
	require.Equal(t, []any{}, entry.Rows.([]any)[0])
	require.Equal(t, 3, entry.SourceRowCount)
	require.True(t, entry.ProjectionComplete)
}

func TestStructuredQueryRowCompanionRecognizesGatewayAndExcludesNonQueryResults(t *testing.T) {
	rows := structuredQueryRowsFixture(3)
	validData := map[string]any{"status": "ok", "dataset_id": "dataset-gateway", "truncated": false, "rows": rows}
	gateway, gatewayMessage := structuredQueryFixture(t, "structured_data", "gateway-query", map[string]any{"operation": "query_series", "arguments": map[string]any{"operation": "rows"}}, validData, true)

	failed, failedMessage := structuredQueryFixture(t, "structured_query_series", "failed-query", nil, validData, false)
	schema, schemaMessage := structuredQueryFixture(t, "structured_data", "schema-only", map[string]any{"operation": "describe_tools"}, validData, true)
	planCall, planMessage := structuredQueryFixture(t, "model_plan", "plan-json", nil, validData, true)
	overriddenIdentity, overriddenMessage := structuredQueryFixture(t, "structured_query_series", "overridden-target", nil, validData, true)
	overriddenIdentity.Target = &types.ToolCallTarget{Name: "describe_tools"}
	document := types.ToolCall{Name: "read_document", ID: "document-json", Result: &types.ToolResult{Success: true, Data: validData}}
	docMessageJSON, err := json.Marshal(map[string]any{"success": true, "data": validData})
	require.NoError(t, err)
	docMessage := chat.Message{Role: "tool", Name: "read_document", ToolCallID: document.ID, Content: string(docMessageJSON)}

	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{gateway, failed, schema, planCall, overriddenIdentity, document}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{gatewayMessage, failedMessage, schemaMessage, planMessage, overriddenMessage, docMessage}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 1)
	require.Equal(t, "gateway-query", entries[0].ToolCallID)
	require.Equal(t, reviewEvidenceID(gatewayMessage), entries[0].EvidenceID)
}

func TestStructuredQueryRowCompanionKeepsMultipleQueriesSeparate(t *testing.T) {
	dataA := map[string]any{"status": "ok", "dataset_id": "dataset-a", "truncated": false, "rows": structuredQueryRowsFixture(3)}
	dataB := map[string]any{"status": "ok", "dataset_id": "dataset-b", "truncated": false, "rows": structuredQueryRowsFixture(2)}
	callA, messageA := structuredQueryFixture(t, "structured_query_series", "query-a", nil, dataA, true)
	callB, messageB := structuredQueryFixture(t, "structured_query_series", "query-b", nil, dataB, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{callA, callB}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{messageA, messageB}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 2)
	require.Equal(t, 0, entries[0].EntryIndex)
	require.Equal(t, 1, entries[1].EntryIndex)
	require.Equal(t, "dataset-a", entries[0].Metadata["dataset_id"])
	require.Equal(t, "dataset-b", entries[1].Metadata["dataset_id"])
	require.NotEqual(t, entries[0].EvidenceID, entries[1].EvidenceID)
	require.Len(t, entries[0].Rows.([]any), 3)
	require.Len(t, entries[1].Rows.([]any), 2)
}

func TestStructuredQueryRowCompanionDeduplicatesOnlyIdenticalObservations(t *testing.T) {
	data := map[string]any{"status": "ok", "dataset_id": "dataset-a", "filters": map[string]any{"start": "2020-01-01"}, "truncated": false, "rows": structuredQueryRowsFixture(2)}
	callA, messageA := structuredQueryFixture(t, "structured_query_series", "query-a", nil, data, true)
	callB, messageB := structuredQueryFixture(t, "structured_query_series", "query-b", nil, data, true)
	// A different filter is a distinct executed result even if it happens to
	// return the same row values.
	otherData := map[string]any{"status": "ok", "dataset_id": "dataset-a", "filters": map[string]any{"start": "2020-02-01"}, "truncated": false, "rows": structuredQueryRowsFixture(2)}
	callC, messageC := structuredQueryFixture(t, "structured_query_series", "query-c", nil, otherData, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{callA, callB, callC}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{messageA, messageB, messageC}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 2)
	require.Equal(t, "query-a", entries[0].ToolCallID)
	require.Equal(t, 0, entries[0].EntryIndex)
	require.Equal(t, 1, entries[0].DuplicateObservations)
	require.Equal(t, reviewEvidenceID(messageA), entries[0].EvidenceID)
	require.Equal(t, "query-c", entries[1].ToolCallID)
	require.Equal(t, 1, entries[1].EntryIndex)
	require.Zero(t, entries[1].DuplicateObservations)
}

func TestStructuredQueryRowCompanionEnforcesExactUTF8ByteBudgetAndPointers(t *testing.T) {
	rows := []any{}
	for i := 0; i < 10; i++ {
		rows = append(rows, map[string]any{"cell": fmt.Sprintf("D%d", i+1), "value": fmt.Sprintf("%s-%d", strings.Repeat("研究数据", 700), i)})
	}
	data := map[string]any{"status": "ok", "dataset_id": "large", "truncated": false, "rows": rows}
	call, message := structuredQueryFixture(t, "structured_query_series", "query-large", nil, data, true)
	view := structuredQueryRowReviewEvidence(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.True(t, utf8.Valid(encoded))
	require.False(t, view["projection_complete"].(bool))
	entry := entries[0]
	require.Less(t, entry.RetainedRowCount, entry.SourceRowCount)
	require.Equal(t, []structuredQueryRowOmission{{JSONPointer: "/data/rows", StartIndex: entry.RetainedRowCount, EndIndexExclusive: len(rows), NextStartIndex: entry.RetainedRowCount}}, entry.OmittedRowRanges)
	require.Equal(t, "/data/rows", entry.SourceJSONPointer)
}

func TestStructuredQueryRowCompanionKeepsDifferentExecutionArguments(t *testing.T) {
	data := map[string]any{"status": "ok", "dataset_id": "dataset-a", "truncated": false, "rows": structuredQueryRowsFixture(2)}
	callA, messageA := structuredQueryFixture(t, "structured_query_series", "query-scope-a", map[string]any{"start_date": "2020-01-01"}, data, true)
	callB, messageB := structuredQueryFixture(t, "structured_query_series", "query-scope-b", map[string]any{"start_date": "2021-01-01"}, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{callA, callB}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{messageA, messageB}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 2, "identical returned data does not merge distinct executed scopes")
	require.Equal(t, reviewEvidenceID(messageA), entries[0].EvidenceID)
	require.Equal(t, reviewEvidenceID(messageB), entries[1].EvidenceID)
	require.Zero(t, entries[0].DuplicateObservations)
	require.Zero(t, entries[1].DuplicateObservations)
}
