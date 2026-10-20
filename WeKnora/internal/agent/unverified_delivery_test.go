package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnverifiedDeliveryNeverKeepsNumericFailuresOrEmptyCandidates(t *testing.T) {
	e := &AgentEngine{}
	e.rememberUnverifiedCandidate("A grounded draft.", candidateDecision{Disposition: candidateRevise})
	e.answerReviewFeedback = "ignored after capture"
	delivery, ok := e.unverifiedDelivery()
	require.True(t, ok)
	require.Contains(t, delivery, "A grounded draft.")
	require.Contains(t, delivery, "未能完成自动核验")

	e = &AgentEngine{}
	e.rememberUnverifiedCandidate("Revenue was 999.", candidateDecision{Disposition: candidateNumericFailure, Unsupported: []string{"999"}})
	e.rememberUnverifiedCandidate("   ", candidateDecision{Disposition: candidateRevise})
	e.rememberUnverifiedCandidate("Approved text.", candidateDecision{Disposition: candidateAccepted})
	_, ok = e.unverifiedDelivery()
	require.False(t, ok, "numeric failures, empty and accepted candidates never become an unverified delivery")
}

func TestUnverifiedDeliveryNeverShowsAuthorRepairAdvice(t *testing.T) {
	e := &AgentEngine{}
	e.answerReviewFeedback = "请基于实际图像重新描述末端水平并据此改写结论。"
	e.rememberUnverifiedCandidate("Draft.", candidateDecision{Disposition: candidateRevise})
	delivery, ok := e.unverifiedDelivery()
	require.True(t, ok)
	require.NotContains(t, delivery, "重新描述", "repair advice addressed to the author is never shown to users")
	require.Contains(t, delivery, "未能完成自动核验")
	require.Contains(t, delivery, "请以引用的原文、原图或数据为准")

	e = &AgentEngine{}
	e.answerReviewFeedback = "请改写第二段。"
	e.answerReviewUserNotice = "图中末端读数与上一轮回答不一致，建议查看原图核对。"
	e.rememberUnverifiedCandidate("Draft.", candidateDecision{Disposition: candidateRevise})
	delivery, ok = e.unverifiedDelivery()
	require.True(t, ok)
	require.True(t, strings.HasPrefix(delivery, "Draft."))
	require.Contains(t, delivery, "图中末端读数与上一轮回答不一致，建议查看原图核对。")
	require.NotContains(t, delivery, "请改写第二段")
}

func TestSourceReviewUserNoticeIsOptionalPresentationText(t *testing.T) {
	notice := strings.Repeat("核", sourceReviewUserNoticeLimit)
	for name, tc := range map[string]struct {
		verdict string
		want    string
	}{
		"kept":     {`{"complete":false,"feedback":"修正读数。","user_notice":"` + notice + `"}`, notice},
		"overlong": {`{"complete":false,"feedback":"修正读数。","user_notice":"` + notice + `核"}`, ""},
		"absent":   {`{"complete":false,"feedback":"修正读数。"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(tc.verdict)}}
			engine := sourceConsistencyEngine(t, model)
			repair, err := engine.answerNeedsRepair(t.Context(), "说明图表。", "候选答复。", "", sourceConsistencyState())
			require.NoError(t, err, "an overlong user notice never invalidates the verdict")
			require.True(t, repair)
			require.Len(t, model.calls, 1)
			require.Equal(t, "修正读数。", engine.answerReviewFeedback)
			require.Equal(t, tc.want, engine.answerReviewUserNotice)
		})
	}
	var schema struct {
		Properties map[string]struct {
			MaxLength int `json:"maxLength"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(sourceConsistencySchema, &schema))
	require.Equal(t, sourceReviewUserNoticeLimit, schema.Properties["user_notice"].MaxLength)
}
