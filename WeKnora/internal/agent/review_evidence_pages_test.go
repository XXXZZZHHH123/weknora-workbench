package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func pagedReviewJSONFixture(t *testing.T, bodyRunes int) (*reviewEvidenceReader, string, string) {
	t.Helper()
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "source-call", Content: fmt.Sprintf(
		`{"success":true,"data":{"body":%q,"exact_decimal":12345678901234567890.00100,"label":"版本v38"}}`, strings.Repeat("棉", bodyRunes))}
	reader := newReviewEvidenceReader([]chat.Message{message})
	value, err := reviewPointerValue(reader.records[0].Value, "/data")
	require.NoError(t, err)
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return reader, reader.records[0].ID, string(encoded)
}

func reviewJSONPagesFixture(t *testing.T, data map[string]any) (*reviewEvidenceReader, string) {
	t.Helper()
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "paged-source-call",
		Content: `{"success":true,"data":` + string(mustReviewJSON(t, data)) + `}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	return reader, reader.records[0].ID
}

func mustReviewJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func appendReviewReadPage(t *testing.T, reader *reviewEvidenceReader, evidenceID, pointer string, offset int, mutateArgs func(map[string]any), mutateResponse func(map[string]any)) string {
	t.Helper()
	callID := fmt.Sprintf("read-%d", len(reader.transcript))
	args := map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": pointer, "offset": offset, "max_chars": reviewEvidenceSegmentLimit}
	if mutateArgs != nil {
		mutateArgs(args)
	}
	encodedArgs, err := json.Marshal(args)
	require.NoError(t, err)
	output := reader.read(string(encodedArgs))
	if mutateResponse != nil {
		mutateResponse(output)
	}
	encodedOutput, err := json.Marshal(output)
	require.NoError(t, err)
	reader.transcript = append(reader.transcript,
		chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(encodedArgs)}}}},
		chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(encodedOutput)},
	)
	return callID
}

func TestCompletedReviewJSONReadsReassemblesExactUnicodePages(t *testing.T) {
	reader, evidenceID, serialized := pagedReviewJSONFixture(t, 12500)
	length := len([]rune(serialized))
	require.Greater(t, length, 2*reviewEvidenceSegmentLimit)
	require.LessOrEqual(t, length, reviewEvidenceRuneBudget)
	for offset := 0; offset < length; offset += reviewEvidenceSegmentLimit {
		appendReviewReadPage(t, reader, evidenceID, "/data", offset, nil, nil)
	}

	completed := completedReviewJSONReads(reader)
	require.Len(t, completed, 1)
	require.Equal(t, evidenceID, completed[0].EvidenceID)
	require.Equal(t, "/data", completed[0].JSONPointer)
	encoded, err := json.Marshal(completed[0].Value)
	require.NoError(t, err)
	require.Equal(t, serialized, string(encoded))
	value := completed[0].Value.(map[string]any)
	require.Equal(t, json.Number("12345678901234567890.00100"), value["exact_decimal"])
	require.Equal(t, strings.Repeat("棉", 12500), value["body"])
}

func TestCompletedReviewJSONReadsRejectsMissingOrTamperedPages(t *testing.T) {
	t.Run("missing page leaves a gap", func(t *testing.T) {
		reader, id, _ := pagedReviewJSONFixture(t, 12500)
		appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
		appendReviewReadPage(t, reader, id, "/data", 2*reviewEvidenceSegmentLimit, nil, nil)
		require.Empty(t, completedReviewJSONReads(reader))
	})

	for name, mutate := range map[string]func(map[string]any){
		"wrong source identity": func(response map[string]any) { response["evidence_id"] = "another-source" },
		"wrong pointer":         func(response map[string]any) { response["json_pointer"] = "/data/body" },
		"wrong offset":          func(response map[string]any) { response["content_offset"] = json.Number("1") },
		"wrong content length":  func(response map[string]any) { response["content_length"] = json.Number("999") },
		"wrong offset space":    func(response map[string]any) { response["offset_space"] = "unicode_text_value" },
		"wrong segment": func(response map[string]any) {
			segment := response["serialized_json_segment"].(string)
			runes := []rune(segment)
			runes[0] = 'X'
			response["serialized_json_segment"] = string(runes)
		},
		"failed read response": func(response map[string]any) { response["status"] = "invalid_pointer" },
	} {
		t.Run(name, func(t *testing.T) {
			reader, id, serialized := pagedReviewJSONFixture(t, 12500)
			length := len([]rune(serialized))
			for offset := 0; offset < length; offset += reviewEvidenceSegmentLimit {
				var change func(map[string]any)
				if offset == reviewEvidenceSegmentLimit {
					change = mutate
				}
				appendReviewReadPage(t, reader, id, "/data", offset, nil, change)
			}
			require.Empty(t, completedReviewJSONReads(reader))
		})
	}
}

func TestCompletedReviewJSONReadsRequiresPairedReadCallsAndExactArgs(t *testing.T) {
	t.Run("unpaired response", func(t *testing.T) {
		reader, id, _ := pagedReviewJSONFixture(t, 100)
		args := `{"operation":"read","evidence_id":"` + id + `","json_pointer":"/data","offset":0,"max_chars":6000}`
		response := reader.read(args)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		reader.transcript = append(reader.transcript, chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "orphan", Content: string(encoded)})
		require.Empty(t, completedReviewJSONReads(reader))
	})

	t.Run("call arguments and response offset differ", func(t *testing.T) {
		reader, id, serialized := pagedReviewJSONFixture(t, 12500)
		length := len([]rune(serialized))
		for offset := 0; offset < length; offset += reviewEvidenceSegmentLimit {
			if offset == reviewEvidenceSegmentLimit {
				appendReviewReadPage(t, reader, id, "/data", offset,
					nil, func(response map[string]any) { response["content_offset"] = json.Number(strconv.Itoa(offset - 1)) })
				continue
			}
			appendReviewReadPage(t, reader, id, "/data", offset, nil, nil)
		}
		require.Empty(t, completedReviewJSONReads(reader))
	})

	t.Run("non-read operation", func(t *testing.T) {
		reader, id, _ := pagedReviewJSONFixture(t, 100)
		args := `{"operation":"list","evidence_id":"` + id + `","json_pointer":"/data","offset":0,"max_chars":6000}`
		callID := "list-call"
		encodedArgs := args
		response := reader.read(encodedArgs)
		encodedResponse, err := json.Marshal(response)
		require.NoError(t, err)
		reader.transcript = append(reader.transcript,
			chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: encodedArgs}}}},
			chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(encodedResponse)},
		)
		require.Empty(t, completedReviewJSONReads(reader))
	})
}

func TestCompletedReviewJSONReadsEnforcesOriginalBudgets(t *testing.T) {
	t.Run("five reader calls rejected", func(t *testing.T) {
		reader, id, _ := pagedReviewJSONFixture(t, 100)
		for i := 0; i < reviewEvidenceReadLimit+1; i++ {
			appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
		}
		require.Empty(t, completedReviewJSONReads(reader))
	})

	t.Run("over-budget serialized value cannot complete", func(t *testing.T) {
		reader, id, serialized := pagedReviewJSONFixture(t, reviewEvidenceRuneBudget+100)
		length := len([]rune(serialized))
		for offset := 0; offset < length && reader.remaining > 0; offset += reviewEvidenceSegmentLimit {
			appendReviewReadPage(t, reader, id, "/data", offset, nil, nil)
		}
		require.LessOrEqual(t, reviewEvidenceRuneBudget-reader.remaining, reviewEvidenceRuneBudget)
		require.Empty(t, completedReviewJSONReads(reader))
	})
}

func TestCompletedReviewJSONReadsRejectsFailedAndSchemaResults(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"failed source result": func(payload map[string]any) { payload["success"] = false },
		"tool schema result": func(payload map[string]any) {
			payload["data"].(map[string]any)["operation"] = "describe_tools"
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader, id, _ := pagedReviewJSONFixture(t, 12500)
			mutate(reader.records[0].Value.(map[string]any))
			value, err := reviewPointerValue(reader.records[0].Value, "/data")
			require.NoError(t, err)
			serialized, err := json.Marshal(value)
			require.NoError(t, err)
			length := len([]rune(string(serialized)))
			for offset := 0; offset < length; offset += reviewEvidenceSegmentLimit {
				appendReviewReadPage(t, reader, id, "/data", offset, nil, nil)
			}
			require.Empty(t, completedReviewJSONReads(reader))
		})
	}
}

func TestReviewWitnessVisibleInJSONPagesAcceptsFullyVisibleChildOfPartialParent(t *testing.T) {
	reader, id := reviewJSONPagesFixture(t, map[string]any{
		"datasets": []any{map[string]any{
			"dataset_id":           "snapshot-identity",
			"is_latest_for_source": true,
			"pending_sheet_states": []any{map[string]any{
				"name": "Measurements", "status": "needs_review", "reasons": []any{"field mapping requires confirmation"},
			}},
		}},
		"long_tail": strings.Repeat("未读取尾段", 4000),
	})
	appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
	witness := reviewSourceWitness{EvidenceID: id, JSONPointer: "/data/datasets/0/pending_sheet_states/0/reasons/0",
		Quote: "field mapping requires confirmation", Kind: "source"}
	require.True(t, reviewWitnessVisibleInJSONPages(reader, witness))
	require.NoError(t, reviewWitnessVisible(nil, reader, witness))
	require.Empty(t, completedReviewJSONReads(reader))
	// A sibling outside the exposed prefix remains hidden even though its parent was read.
	witness.JSONPointer = "/data/long_tail"
	witness.Quote = "未读取尾段"
	require.False(t, reviewWitnessVisibleInJSONPages(reader, witness))
}

func TestReviewWitnessVisibleInJSONPagesUsesPathSpanNotRepeatedTextSearch(t *testing.T) {
	reader, id := reviewJSONPagesFixture(t, map[string]any{
		"a_visible": map[string]any{
			"0status":    "needs_review",
			"zz_padding": strings.Repeat("填", 6200),
		},
		"z_target": map[string]any{"status": "needs_review"},
		"zz_tail":  strings.Repeat("尾", 5000),
	})
	appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
	require.True(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/a_visible/0status", Quote: "needs_review", Kind: "source",
	}))
	require.False(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/z_target/status", Quote: "needs_review", Kind: "source",
	}))
}

func TestReviewWitnessVisibleInJSONPagesHandlesEscapedKeysUnicodeOffsetsAndExactNumbers(t *testing.T) {
	reader, id := reviewJSONPagesFixture(t, map[string]any{
		"a/tilde~key": map[string]any{
			"label": "棉花状态",
			"value": json.Number("12345678901234567890.00100"),
		},
		"zz_tail": strings.Repeat("尾", 7000),
	})
	appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
	require.True(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/a~1tilde~0key/label", Quote: "棉花状态", Kind: "source",
	}))
	require.True(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/a~1tilde~0key/value", Quote: "12345678901234567890.00100", Kind: "source",
	}))
	require.False(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/a~1tilde~0key/value", Quote: "12345678901234567890.001", Kind: "source",
	}))
}

func TestReviewWitnessVisibleInJSONPagesDoesNotBridgeUnreadGap(t *testing.T) {
	reader, id := reviewJSONPagesFixture(t, map[string]any{
		"a_prefix": strings.Repeat("前", 7000),
		"b_target": map[string]any{"status": "needs_review"},
		"z_tail":   strings.Repeat("尾", 5000),
	})
	appendReviewReadPage(t, reader, id, "/data", 0, nil, nil)
	appendReviewReadPage(t, reader, id, "/data", 2*reviewEvidenceSegmentLimit, nil, nil)
	require.False(t, reviewWitnessVisibleInJSONPages(reader, reviewSourceWitness{
		EvidenceID: id, JSONPointer: "/data/b_target/status", Quote: "needs_review", Kind: "source",
	}))
}
