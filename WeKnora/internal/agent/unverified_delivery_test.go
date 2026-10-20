package agent

import (
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

func TestUnverifiedDeliveryBoundsDisclosedReviewConcern(t *testing.T) {
	e := &AgentEngine{}
	long := make([]rune, 1000)
	for i := range long {
		long[i] = '疑'
	}
	e.answerReviewFeedback = string(long)
	e.rememberUnverifiedCandidate("Draft.", candidateDecision{Disposition: candidateRevise})
	delivery, ok := e.unverifiedDelivery()
	require.True(t, ok)
	require.Less(t, len([]rune(delivery)), 400)
}
