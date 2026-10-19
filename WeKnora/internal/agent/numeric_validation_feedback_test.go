package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestNumericValidationFeedbackUsesExactTokenSpans(t *testing.T) {
	content := "峰值138.67；区间119–138；另一项1380。"
	feedback := buildNumericValidationFeedback(content, []string{"138", "138"})
	encoded, err := json.Marshal(feedback)
	require.NoError(t, err)
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(encoded, &view))
	require.Equal(t, "unsupported_tokens", view.CheckStatus)
	require.Equal(t, 1, view.UnsupportedTokenCount, "duplicate guard tokens are deduplicated")
	require.Len(t, view.UnsupportedTokens, 1)
	issue := view.UnsupportedTokens[0]
	require.Equal(t, "138", issue.Token)
	require.Len(t, issue.CandidateOccurrences, 1, "138.67 and 1380 are separate parser tokens")
	occurrence := issue.CandidateOccurrences[0]
	require.Equal(t, strings.Index(content, "119–138")+len("119–"), occurrence.StartByte)
	require.Equal(t, occurrence.StartByte+3, occurrence.EndByte)
	require.Equal(t, utf8.RuneCountInString(content[:occurrence.StartByte]), occurrence.StartRune)
	require.Equal(t, occurrence.StartRune+3, occurrence.EndRune)
	require.Contains(t, occurrence.Context, "119–138")
	require.Contains(t, view.Notice, "lexical")
	require.LessOrEqual(t, len(encoded), numericValidationFeedbackBudget)
}

func TestNumericValidationFeedbackDoesNotLocateShortTokenInsideLongerDecimal(t *testing.T) {
	feedback := buildNumericValidationFeedback("记录峰值138.67。", []string{"138"})
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, feedback), &view))
	require.Len(t, view.UnsupportedTokens, 1)
	require.Empty(t, view.UnsupportedTokens[0].CandidateOccurrences)
	require.True(t, view.UnsupportedTokens[0].NoLexicalCandidateLocated)
}

func TestNumericValidationFeedbackMapsRepeatedDecimalsPercentAndOpaqueIDs(t *testing.T) {
	content := "首个138.67，其次138；尾数138。比例13.8%。引用ID deadbeef-1234-4abc-9def-0123456789ab。"
	feedback := buildNumericValidationFeedback(content, []string{"138", "13.8", "deadbeef-1234-4abc-9def-0123456789ab"})
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, feedback), &view))
	require.Equal(t, 3, view.UnsupportedTokenCount)
	require.Len(t, view.UnsupportedTokens[0].CandidateOccurrences, 2)
	require.Equal(t, "138", view.UnsupportedTokens[0].Token)
	require.Equal(t, "13.8", view.UnsupportedTokens[1].Token)
	require.Len(t, view.UnsupportedTokens[1].CandidateOccurrences, 1)
	percent := view.UnsupportedTokens[1].CandidateOccurrences[0]
	require.Equal(t, "13.8", content[percent.StartByte:percent.EndByte])
	require.Contains(t, percent.Context, "13.8%")
	require.Equal(t, "opaque_identifier", view.UnsupportedTokens[2].TokenKind)
	require.Len(t, view.UnsupportedTokens[2].CandidateOccurrences, 1)
	for _, occurrence := range view.UnsupportedTokens[0].CandidateOccurrences {
		require.Equal(t, "138", content[occurrence.StartByte:occurrence.EndByte])
	}
	require.Contains(t, view.Notice, "not source evidence")
}

func TestNumericValidationFeedbackUnicodeOffsetsAndBoundedContext(t *testing.T) {
	prefix := strings.Repeat("🧭研究记录", 50)
	content := prefix + "未匹配值138及后续说明。"
	feedback := buildNumericValidationFeedback(content, []string{"138"})
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, feedback), &view))
	require.Len(t, view.UnsupportedTokens, 1)
	occurrence := view.UnsupportedTokens[0].CandidateOccurrences[0]
	require.Equal(t, utf8.RuneCountInString(content[:occurrence.StartByte]), occurrence.StartRune)
	require.Equal(t, occurrence.StartRune+3, occurrence.EndRune)
	require.Equal(t, "138", content[occurrence.StartByte:occurrence.EndByte])
	require.LessOrEqual(t, utf8.RuneCountInString(occurrence.Context), numericValidationFeedbackContextLimit)
	require.Equal(t, string([]rune(content)[occurrence.ContextStartRune:occurrence.ContextEndRune]), occurrence.Context)
	require.True(t, occurrence.ContextTruncatedLeft)
}

func TestNumericValidationFeedbackHasNoUnmatchedStatusAndCandidateDigest(t *testing.T) {
	content := "稳定的完整答复。"
	feedback := buildNumericValidationFeedback(content, nil)
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, feedback), &view))
	require.Equal(t, "no_unmatched_numbers", view.CheckStatus)
	require.Empty(t, view.UnsupportedTokens)
	require.Zero(t, view.UnsupportedTokenCount)
	digest := sha256.Sum256([]byte(content))
	require.Equal(t, hex.EncodeToString(digest[:]), view.CandidateSHA256)
	require.Contains(t, view.Notice, "not source evidence")
}

func TestNumericValidationFeedbackEnforcesSerializedBudgetAndReportsOmissions(t *testing.T) {
	var tokens []string
	var parts []string
	for i := 100; i < 108; i++ {
		token := strings.Repeat("7", i-99)
		tokens = append(tokens, token)
		parts = append(parts, strings.Repeat("界", 400)+token+strings.Repeat("界", 400), strings.Repeat("界", 400)+token+strings.Repeat("界", 400))
	}
	feedback := buildNumericValidationFeedback(strings.Join(parts, "；"), tokens)
	encoded, err := json.Marshal(feedback)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), numericValidationFeedbackBudget)
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(encoded, &view))
	require.Equal(t, len(tokens), view.UnsupportedTokenCount)
	require.True(t, view.ContextMaxRunes < numericValidationFeedbackContextLimit || view.OmittedContextCount > 0 || view.OmittedUnsupportedTokenCount > 0,
		"the adversarial Unicode excerpts should force explicit budget trimming")
	for _, issue := range view.UnsupportedTokens {
		require.GreaterOrEqual(t, issue.OmittedOccurrenceCount, 0)
	}
}

func TestNumericValidationFeedbackContextInheritsCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	feedback := buildNumericValidationFeedback("结果138。", []string{"138"})
	ctx := withNumericValidationFeedback(base, feedback)
	require.NotNil(t, numericValidationFeedbackFromContext(ctx))
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("wrapped context must retain cancellation")
	}
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, "unsupported_tokens", numericValidationFeedbackFromContext(ctx)["check_status"])
}

func TestNumericValidationFeedbackContextDeepCopiesNestedValues(t *testing.T) {
	base := context.Background()
	feedback := buildNumericValidationFeedback("结果138。", []string{"138"})
	ctx := withNumericValidationFeedback(base, feedback)

	original := numericValidationFeedbackFromContext(ctx)
	issues := original["unsupported_tokens"].([]any)
	issue := issues[0].(map[string]any)
	occurrences := issue["candidate_occurrences"].([]any)
	occurrence := occurrences[0].(map[string]any)
	original["check_status"] = "changed"
	occurrence["context"] = "changed"

	fromContext := numericValidationFeedbackFromContext(ctx)
	require.Equal(t, "unsupported_tokens", fromContext["check_status"])
	issues = fromContext["unsupported_tokens"].([]any)
	issue = issues[0].(map[string]any)
	occurrences = issue["candidate_occurrences"].([]any)
	occurrence = occurrences[0].(map[string]any)
	require.NotEqual(t, "changed", occurrence["context"])
}

func TestSourceCriticReceivesNumericDiagnosticSeparatelyFromSourceEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"请修正未匹配的统计区间。","next_action":"revise_answer"}`)}}
	engine := sourceConsistencyEngine(t, model)
	content := "多数月份维持在119–138高位，峰值138.67。"
	feedback := buildNumericValidationFeedback(content, []string{"138"})
	ctx := withNumericValidationFeedback(context.Background(), feedback)
	conversation := []chat.Message{{Role: "system", Content: "Use actual evidence."}, {Role: "user", Content: "Summarize the research trend."}}
	_, err := engine.evaluateAnswerCompletenessOnce(ctx, "Summarize the research trend.", content, sourceConsistencyState(), 0, conversation)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	var got numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, input["deterministic_numeric_validation"]), &got))
	require.Equal(t, "unsupported_tokens", got.CheckStatus)
	require.Equal(t, feedback["candidate_sha256"], got.CandidateSHA256)
	require.NotContains(t, numericFeedbackTestJSON(t, input["source_tool_results"]), "deterministic_numeric_validation",
		"program feedback remains a distinct typed input, not a source result")
}

func TestSourceCriticOmitsStaleNumericDiagnostic(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	ctx := withNumericValidationFeedback(context.Background(), buildNumericValidationFeedback("候选甲138。", []string{"138"}))
	content := "不同候选乙139。"
	conversation := []chat.Message{{Role: "system", Content: "Use actual evidence."}, {Role: "user", Content: "Summarize the research trend."}}
	_, err := engine.evaluateAnswerCompletenessOnce(ctx, "Summarize the research trend.", content, sourceConsistencyState(), 0, conversation)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	require.NotContains(t, input, "deterministic_numeric_validation",
		"diagnostics are bound to the candidate hash and cannot leak onto a later draft")
}

func numericFeedbackTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}

func TestNumericValidationFeedbackKeepsLargeDecimalCountAsNumber(t *testing.T) {
	const count = "123456789012345678901234"
	feedback := buildNumericValidationFeedback("The observed particle count is "+count+".", []string{count})
	var view numericValidationFeedbackView
	require.NoError(t, json.Unmarshal(numericFeedbackTestJSON(t, feedback), &view))
	require.Len(t, view.UnsupportedTokens, 1)
	require.Equal(t, "number", view.UnsupportedTokens[0].TokenKind)
	require.Len(t, view.UnsupportedTokens[0].CandidateOccurrences, 1)
}
