package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type candidateDisposition string

const (
	candidateAccepted        candidateDisposition = "accepted"
	candidateRevise          candidateDisposition = "revise"
	candidateNeedSource      candidateDisposition = "need_source"
	candidateNumericFailure  candidateDisposition = "numeric_failure"
	candidateProtocolFailure candidateDisposition = "protocol_failure"
)

// A decision belongs to the actual candidate, human task and immutable tool
// observations sent to the checker. It is never a reusable boolean approval.
// Hashes are assigned by the runtime, not supplied by a model.
type candidateDecision struct {
	Disposition     candidateDisposition
	CandidateHash   string
	TaskHash        string
	EvidenceHash    string
	Unsupported     []string
	NumericFeedback map[string]any
	Err             error
}

type turnReviewState struct {
	Repairs       int
	Checks        int
	Last          candidateDecision
	ReadEvidence  *reviewEvidenceHandoff
	ClosedStreams map[string]bool
	Committed     bool
	Query         string
	Conversation  []chat.Message
	// Latest non-empty candidate that passed deterministic numeric grounding
	// but was not approved by the semantic reviewer, with its last advice.
	UnverifiedCandidate string
	UnverifiedFeedback  string
}

// Preserve the existing two numeric and two semantic repair allowances while
// preventing exceptional synthesis from opening another independent budget.
const maxTurnCandidateRepairs = 4

func (e *AgentEngine) spendCandidateRepair() bool {
	if e.turnReview.Repairs >= maxTurnCandidateRepairs {
		return false
	}
	e.turnReview.Repairs++
	return true
}

func candidateTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func reviewDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// checkCandidate is shared by ordinary completion, stalled-turn recovery and
// exceptional synthesis. Numeric provenance precedes semantic review everywhere.
// forceReview retains the existing exceptional-path completeness policy.
func (e *AgentEngine) checkCandidate(ctx context.Context, query, candidate, reasoning string, state *types.AgentState, conversation []chat.Message, forceReview bool) (decision candidateDecision) {
	e.turnReview.ReadEvidence = nil
	e.turnReview.Query = query
	e.turnReview.Conversation = append([]chat.Message(nil), conversation...)
	e.answerReviewFeedback = ""
	e.answerReviewAction = ""
	decision = candidateDecision{Disposition: candidateAccepted,
		CandidateHash: candidateTextDigest(candidate),
		TaskHash: reviewDigest(struct {
			Query        string
			Conversation []chat.Message
		}{query, e.semanticReviewConversation(conversation)}),
		EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, conversation)),
	}
	defer func() {
		e.turnReview.Checks++
		e.turnReview.Last = decision
		if e.needsSourceConsistencyCheck(state) && !suspectAnswerFragment(candidate, reasoning) {
			// Only candidates judged by the source critic qualify; completeness
			// checks of fragments or non-sourced replies never become deliveries.
			e.rememberUnverifiedCandidate(candidate, decision)
		}
		common.PipelineInfo(ctx, "Agent", "candidate_check", map[string]interface{}{
			"disposition": decision.Disposition, "candidate_hash": decision.CandidateHash,
			"task_revision": decision.TaskHash, "evidence_revision": decision.EvidenceHash,
			"checks": e.turnReview.Checks, "repairs": e.turnReview.Repairs,
		})
	}()
	if ctx.Err() != nil {
		decision.Disposition = candidateProtocolFailure
		decision.Err = ctx.Err()
		return
	}
	if strings.TrimSpace(candidate) == "" {
		decision.Disposition = candidateRevise
		return
	}
	if needsStructuredAnswerReviewForQuery(query, state.RoundSteps, conversation, candidate) {
		evidence := answerNumericEvidence(candidate, state.RoundSteps, conversation)
		decision.Unsupported = ungroundedStructuredNumbers(candidate, evidence, state.RoundSteps)
		decision.NumericFeedback = buildNumericValidationFeedback(candidate, decision.Unsupported)
		if len(decision.Unsupported) > 0 {
			decision.Disposition = candidateNumericFailure
			return
		}
		ctx = withNumericValidationFeedback(ctx, decision.NumericFeedback)
	}
	// Delivery policy (user decision 2026-10-20): direct lookups are delivered
	// after deterministic numeric grounding, like a single-model answer with
	// citations; the independent critic is reserved for synthesis tasks, where
	// it caught real cross-source and trend errors. Without a planner judgement
	// the critic still runs; fragments still get the completeness check.
	if e.needsSourceConsistencyCheck(state) && e.taskReasoningDepth() == "direct" && !suspectAnswerFragment(candidate, reasoning) {
		common.PipelineInfo(ctx, "Agent", "source_review_skipped", map[string]interface{}{
			"reason": "direct_task", "reasoning_depth": e.taskReasoningDepth(),
		})
		return
	}
	var repair bool
	if forceReview {
		repair, decision.Err = e.evaluateAnswerCompleteness(ctx, query, candidate, state, conversation)
	} else {
		repair, decision.Err = e.answerNeedsRepair(ctx, query, candidate, reasoning, state, conversation)
	}
	if decision.Err != nil {
		decision.Disposition = candidateProtocolFailure
	} else if repair {
		decision.Disposition = candidateRevise
		if e.answerReviewAction == reviewCollectEvidence {
			decision.Disposition = candidateNeedSource
		}
	}
	return
}

// Recheck version identity at the delivery boundary. No model call is reused
// when the human task or actual observations changed after its approval.
func (e *AgentEngine) candidateApprovalCurrent(candidate string, state *types.AgentState) bool {
	d := e.turnReview.Last
	return d.Disposition == candidateAccepted && d.CandidateHash == candidateTextDigest(candidate) &&
		d.TaskHash == reviewDigest(struct {
			Query        string
			Conversation []chat.Message
		}{e.turnReview.Query, e.semanticReviewConversation(e.turnReview.Conversation)}) &&
		d.EvidenceHash == reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation))
}

// rememberUnverifiedCandidate keeps the newest candidate that reached semantic
// review (so numeric grounding already passed) and was not approved. Numeric
// failures and empty candidates are never kept.
func (e *AgentEngine) rememberUnverifiedCandidate(candidate string, decision candidateDecision) {
	if strings.TrimSpace(candidate) == "" || len(decision.Unsupported) > 0 {
		return
	}
	switch decision.Disposition {
	case candidateRevise, candidateNeedSource, candidateProtocolFailure:
	default:
		return
	}
	if decoded, ok := decodeReviewedAnswerEnvelope(candidate); ok && strings.TrimSpace(decoded) != "" {
		candidate = decoded
	}
	e.turnReview.UnverifiedCandidate = candidate
	e.turnReview.UnverifiedFeedback = strings.TrimSpace(e.answerReviewFeedback)
}

const unverifiedDeliveryFeedbackLimit = 200

// unverifiedDelivery is the delivery policy chosen by the user (2026-10-20):
// when semantic review fails or keeps rejecting, deliver the latest
// numerically grounded candidate with an explicit notice instead of nothing.
func (e *AgentEngine) unverifiedDelivery() (string, bool) {
	candidate := strings.TrimSpace(e.turnReview.UnverifiedCandidate)
	if candidate == "" {
		return "", false
	}
	notice := "\n\n> 说明：本回答未能完成自动核验，数字已与取数结果核对，但结论表述请结合引用来源判断。"
	if feedback := []rune(e.turnReview.UnverifiedFeedback); len(feedback) > 0 {
		if len(feedback) > unverifiedDeliveryFeedbackLimit {
			feedback = append(feedback[:unverifiedDeliveryFeedbackLimit-1], '…')
		}
		notice += "核验曾提出的疑问（本身也可能有误）：" + string(feedback)
	}
	return candidate + notice, true
}

func (e *AgentEngine) taskReasoningDepth() string {
	if e == nil || e.taskPlan == nil {
		return ""
	}
	return e.taskPlan.ReasoningDepth
}

