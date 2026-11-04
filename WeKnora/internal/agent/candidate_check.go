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
	candidateNumericWarning  candidateDisposition = "numeric_warning"
	candidateProtocolFailure candidateDisposition = "protocol_failure"
)

// A decision belongs to the actual candidate, human task and immutable tool
// observations sent to the checker. It is never a reusable boolean approval.
// Hashes are assigned by the runtime, not supplied by a model.
type candidateDecision struct {
	Disposition     candidateDisposition
	Candidate       string
	CandidateHash   string
	TaskHash        string
	EvidenceHash    string
	Unsupported     []string
	NumericFeedback map[string]any
	Err             error
}

type turnReviewState struct {
	AdditionsCache map[string]string
	Repairs        int
	Checks         int
	Last           candidateDecision
	ReadEvidence   *reviewEvidenceHandoff
	ClosedStreams  map[string]bool
	Committed      bool
	Query          string
	Conversation   []chat.Message
	// Latest numerically grounded semantic fallback, or a source-reviewed
	// numeric-warning delivery. The latter retains the exact checked envelope
	// separately from its deterministic decoded presentation.
	UnverifiedCandidate        string
	UnverifiedCheckedCandidate string
	UnverifiedUserNotice       string
	UnverifiedDecision         candidateDecision
	NumericWarningAllowed      bool
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
	e.answerReviewUserNotice = ""
	e.answerReviewAction = ""
	decision = candidateDecision{Disposition: candidateAccepted,
		Candidate:     candidate,
		CandidateHash: candidateTextDigest(candidate),
		TaskHash:      e.candidateTaskDigest(query, conversation),
		EvidenceHash:  reviewDigest(e.sourceConsistencyMessages(state, conversation)),
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
	// Only the runtime's private receipt for this exact task, result and saved
	// Markdown can reuse the independent pre-render semantic source check.
	// Public success/accepted fields and a merely existing image cannot do so.
	if e.isVerifiedChartDelivery(query, candidate, state, conversation) {
		common.PipelineInfo(ctx, "Agent", "source_review_skipped", map[string]interface{}{"reason": "verified_chart_delivery"})
		return
	}
	candidate = e.selectCandidateAdditions(ctx, query, candidate, state, conversation)
	decision.Candidate, decision.CandidateHash = candidate, candidateTextDigest(candidate)
	if needsStructuredAnswerReviewForQuery(query, state.RoundSteps, conversation, candidate) {
		evidence := answerNumericEvidence(candidate, state.RoundSteps, conversation)
		decision.Unsupported = ungroundedStructuredNumbers(candidate, evidence, state.RoundSteps)
		decision.NumericFeedback = buildNumericValidationFeedback(candidate, decision.Unsupported)
		if len(decision.Unsupported) > 0 && !e.turnReview.NumericWarningAllowed && e.turnReview.Repairs < maxTurnCandidateRepairs {
			decision.Disposition = candidateNumericFailure
			return
		}
		ctx = withNumericValidationFeedback(ctx, decision.NumericFeedback)
	}
	// After the bounded numeric repairs, the matcher remains diagnostic. The
	// source model judges the complete task and actual evidence, including a
	// visible caution, rather than rejecting all supported results for an extra
	// number. This exception never changes ordinary direct-lookup policy.
	warning := len(decision.Unsupported) > 0
	if warning && !e.needsSourceConsistencyCheck(state) {
		decision.Disposition = candidateNumericFailure
		return
	}
	if warning {
		ctx = context.WithValue(ctx, numericWarningNoticeKey{}, numericWarningNotice(decision.Unsupported))
	}
	// Delivery policy (user decision 2026-10-20): direct lookups are delivered
	// after deterministic numeric grounding, like a single-model answer with
	// citations; the independent critic is reserved for synthesis tasks, where
	// it caught real cross-source and trend errors. Without a planner judgement
	// the critic still runs; fragments still get the completeness check. Actual
	// successful PDF SQL analysis retains semantic checking even when the
	// planner initially chose direct lookup; execution does not certify scope.
	// The fast path also requires an explicit text-only delivery contract.
	// Direct reasoning depth alone cannot waive requested artifact completion.
	// With citations enabled, missing/invalid current references still need the
	// existing critic's semantic required/not_required/unavailable decision.
	// This eligibility check neither chooses a source nor infers a user intent.
	if !warning && e.needsSourceConsistencyCheck(state) && e.taskReasoningDepth() == "direct" && e.hasExplicitTextDeliveryPlan() && !suspectAnswerFragment(candidate, reasoning) && !hasExecutedPDFAnalysis(state.RoundSteps) && !e.needsDeliveryReview(state) && e.directCitationDeliveryEligible(candidate, state, conversation) {
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
	} else if warning && e.needsSourceConsistencyCheck(state) {
		decision.Disposition = candidateNumericWarning
	}
	return
}

func (e *AgentEngine) directCitationDeliveryEligible(candidate string, state *types.AgentState, conversation []chat.Message) bool {
	if !e.config.CitationsEnabled() {
		return true
	}
	ids := uniqueCandidateCitationIDs(candidate)
	return len(ids) > 0 && len(e.currentCandidateKnowledgeReferences(candidate, state, conversation)) == len(ids)
}

// hasExecutedPDFAnalysis recognizes only current canonical results from the
// actual authorized PDF SQL operation. It does not inspect the question,
// candidate, SQL text, displayed Output or the planner's proposed tools.
func hasExecutedPDFAnalysis(steps []types.AgentStep) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			data := call.Result.Data
			if data["kind"] != "structured_analysis" || data["status"] != "ok" || data["source_kind"] != "pdf_table" {
				continue
			}
			switch call.ExecutionName() {
			case "structured_query_pdf_table":
				return true
			case "structured_data":
				if call.ExecutionArgs()["operation"] == "query_pdf_table" {
					return true
				}
			}
		}
	}
	return false
}

// Recheck version identity at the delivery boundary. No model call is reused
// when the human task or actual observations changed after its approval.
func (e *AgentEngine) candidateApprovalCurrent(candidate string, state *types.AgentState) bool {
	d := e.turnReview.Last
	return d.Disposition == candidateAccepted && e.candidateDecisionCurrent(d, candidate, state)
}

func (e *AgentEngine) candidateDecisionCurrent(d candidateDecision, candidate string, state *types.AgentState) bool {
	return d.CandidateHash == candidateTextDigest(candidate) &&
		d.TaskHash == e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation) &&
		d.EvidenceHash == reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation))
}

// rememberUnverifiedCandidate keeps the newest candidate that reached semantic
// review and was not approved, or one approved only for numeric-warning
// delivery. Rejected numeric candidates and empty candidates are never kept.
func (e *AgentEngine) rememberUnverifiedCandidate(candidate string, decision candidateDecision) {
	if strings.TrimSpace(candidate) == "" || (len(decision.Unsupported) > 0 && decision.Disposition != candidateNumericWarning) {
		return
	}
	switch decision.Disposition {
	case candidateRevise, candidateNeedSource, candidateProtocolFailure, candidateNumericWarning:
	default:
		return
	}
	e.turnReview.UnverifiedCheckedCandidate = candidate
	if decoded, ok := decodeReviewedAnswerEnvelope(candidate); ok && strings.TrimSpace(decoded) != "" {
		candidate = decoded
	}
	e.turnReview.UnverifiedCandidate = candidate
	e.turnReview.UnverifiedUserNotice = strings.TrimSpace(e.answerReviewUserNotice)
	e.turnReview.UnverifiedDecision = decision
	if decision.Disposition == candidateNumericWarning {
		e.turnReview.UnverifiedUserNotice = numericWarningNotice(decision.Unsupported)
	}
}

type numericWarningNoticeKey struct{}

func numericWarningNotice(tokens []string) string {
	visible := []string{}
	for _, token := range tokens {
		if len(visible) == 4 {
			break
		}
		if len(token) <= 24 && !strings.ContainsAny(token, "\r\n<>") {
			visible = append(visible, token)
		}
	}
	if len(visible) == 0 {
		return "部分数字尚未完全核实，请结合引用的原始资料使用。"
	}
	return "其中“" + strings.Join(visible, "、") + "”尚未完成逐项核实，请结合引用的原始资料使用。"
}

func (e *AgentEngine) numericWarningDelivery(candidate string, state *types.AgentState) (string, bool) {
	d := e.turnReview.UnverifiedDecision
	base := e.turnReview.UnverifiedCandidate
	if d.Disposition != candidateNumericWarning || !e.candidateDecisionCurrent(d, e.turnReview.UnverifiedCheckedCandidate, state) {
		return "", false
	}
	delivery := base + "\n\n> 提示：" + e.turnReview.UnverifiedUserNotice
	if candidate != base && candidate != e.turnReview.UnverifiedCheckedCandidate && candidate != delivery {
		return "", false
	}
	return delivery, true
}

// unverifiedDelivery is the delivery policy chosen by the user (2026-10-20):
// when semantic review fails or keeps rejecting, deliver the latest
// numerically grounded candidate with an explicit notice instead of nothing.
// Reviewer repair advice is written for the author and may itself be wrong, so
// it is never shown; only the reviewer's separate user-facing caution is.
func (e *AgentEngine) unverifiedDelivery() (string, bool) {
	if e.turnReview.UnverifiedDecision.Disposition == candidateNumericWarning {
		return "", false
	}
	candidate := strings.TrimSpace(e.turnReview.UnverifiedCandidate)
	if candidate == "" {
		return "", false
	}
	notice := "\n\n> 提示：本回答未能完成自动核验，部分表述可能与原始资料有出入，请以引用的原文、原图或数据为准。"
	if caution := strings.TrimSpace(e.turnReview.UnverifiedUserNotice); caution != "" {
		notice = "\n\n> 提示：本回答未能完成自动核验。" + caution
	}
	return candidate + notice, true
}

func (e *AgentEngine) taskReasoningDepth() string {
	if e == nil || e.taskPlan == nil {
		return ""
	}
	return e.taskPlan.ReasoningDepth
}
