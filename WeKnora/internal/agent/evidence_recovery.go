package agent

import (
	"context"
	"encoding/json"
	"slices"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const reviewCollectEvidence = "collect_evidence"
const reviewReviseAnswer = "revise_answer"
const maxEvidenceRecoveryAttempts = 2

// The evaluator chooses the disposition from the full task and actual evidence.
// The runtime only bounds recovery and restores the authorized tool loop. It
// never interprets feedback words or turns the evaluator's advice into facts.
func (e *AgentEngine) resumeEvidenceCollection(ctx context.Context, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID string) {
	guards.evidenceRecoveryAttempts++
	e.beginEvidenceCompletionRecovery(ctx, state)
	restoredOriginal := e.restoreEvidenceCollection(messages, guards, evidenceRecoveryInstructions+e.answerRepairAdvice())
	e.emitProgress(ctx, "sources", state.CurrentRound, sessionID)
	common.PipelineInfo(ctx, "Agent", "evidence_recovery", map[string]interface{}{
		"iteration": state.CurrentRound, "attempt": guards.evidenceRecoveryAttempts,
		"disposition":                  reviewCollectEvidence,
		"phase":                        "evidence_collection",
		"incomplete_answer_retries":    guards.incompleteAnswerRetries,
		"original_transcript_restored": restoredOriginal,
	})
}

func (e *AgentEngine) restoreEvidenceCollection(messages *[]chat.Message, guards *loopGuards, notice string) bool {
	restored, restoredOriginal := restoreStructuredReviewConversation(*messages, guards)
	*messages = restored
	if notice != "" {
		*messages = append(*messages, chat.Message{Role: "system", Content: notice})
	}
	if handoff, ok := reviewEvidenceHandoffMessage(e.turnReview.ReadEvidence); ok {
		*messages = append(*messages, handoff)
	}
	e.turnReview.ReadEvidence = nil
	e.structuredReviewMode = false      // Resume the user's actual reasoning settings.
	e.clearTaskToolProjection(messages) // Recovery may need a capability omitted by the initial plan.
	guards.structuredAnswerReviewed = false
	guards.structuredReviewEvidence = nil
	guards.structuredReviewInput = ""
	guards.sawPlainRound = false
	guards.consecutiveSameContent = 0
	e.contextRewrites++
	e.lastUsage = types.TokenUsage{}
	e.lastSentMsgCount = 0
	return restoredOriginal
}

func (e *AgentEngine) sourceReadWindowOpen(ctx context.Context, state *types.AgentState, round int) bool {
	return ctx.Err() == nil && state != nil && state.Suspended == nil && state.SuspendedStep == nil && !state.IsComplete &&
		structuredTaskPlanEnabled() && e.evidenceCompletionEligible() && e.standardTextDelivery(state) && !e.holdCollaborationAnswer &&
		e.withinIterationBudget(round) && e.executionLeaseError(ctx) == nil && !e.collaborationOwnsFailure(ctx) &&
		standardAnswerSourceBudgetReason(state, agentExecutionStart(ctx)) == "" &&
		e.evidenceCompletion.stopReason == ""
}

// A source decision reuses the normal execution loop and its time and round
// limits. A reminder or another draft never records the read as attempted.
// Existing native execution still establishes the actual source and result.
func (e *AgentEngine) resumePendingSourceRead(ctx context.Context, query string, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID string, decision candidateDecision) bool {
	if !e.sourceReadWindowOpen(ctx, state, state.CurrentRound+1) {
		return false
	}
	conversation, _ := restoreStructuredReviewConversation(*messages, guards)
	e.refreshEvidenceCompletion(ctx, query, state, conversation)
	if _, ready := e.nextCoverageLastValidRead(ctx, state); ready {
		// The next iteration can execute this exact read without a model call.
		e.restoreEvidenceCollection(messages, guards, "")
		return true
	}
	var actions []map[string]any
	turn := &e.evidenceCompletion
	issues := e.lastValidSelectionFeedback(ctx, state)
	for _, issue := range issues[:min(len(issues), 8)] {
		actions = append(actions, map[string]any{"action": "bind_series_endpoint", "selection": issue})
	}

	if e.documentTablePlanningEnabled() {
		search, read := e.toolRegistry.DocumentCoverageCapabilities()
		for _, target := range e.evidenceCompletionTargets() {
			record := e.coverageRecordForTarget(ctx, target, turn.record)
			for _, gap := range evaluateTaskCoverage(target, record) {
				if gap.State != coverageOpen {
					continue
				}
				switch {
				case plannedTableComparison(target):
					if action := e.comparisonReadHandoff(ctx, state, target, gap, record, read, decision.Candidate); action != nil {
						actions = append(actions, action)
					}
				case target.SourceMode == "named_first" && e.coverageOnlyDomain() == "" &&
					gap.NextRead != "" && slices.Contains(gap.OpenItems, "attributed_supplement") &&
					(read && (search || gap.NextRead == "read") || e.structuredCoverageReadAvailable(target.GoalIndex)):
					var boundary coverageEvidence
					for _, fact := range gap.PinnedFacts {
						if fact.Role == "coverage_boundary" {
							boundary = fact.Evidence
							break
						}
					}
					if boundary.SeriesID == "" || !boundary.Complete {
						continue
					}
					actions = append(actions, map[string]any{"action": gap.NextRead + "_attributed_supplement", "goal_index": target.GoalIndex,
						"primary_source": boundary.SourceTitle, "primary_field": boundary.Metric,
						"requested_from": boundary.PeriodFrom, "requested_to": boundary.PeriodTo})
				}
			}
		}
	}
	if len(actions) == 0 {
		return false
	}

	encoded, _ := json.Marshal(actions)
	notice := evidenceRecoveryInstructions +
		"Use the normal source tools to address these outstanding reads before concluding whenever the requested source scope permits. " +
		"Keep already verified readings. Read the specific source or original cell; another catalogue response or another draft does not perform that read. " +
		"For a document table, use its actual table object and read the page or original chunk as text/table evidence. " +
		"A pixel preview alone does not verify an attributed table intersection. Only select a different field with the actual source metadata and full task. " +
		"Read any permitted supplement independently of the named primary reading and cite it separately. Pending source actions: " + string(encoded)
	additionalFeedback := e.answerRepairAdvice()
	e.restoreEvidenceCollection(messages, guards, "")
	guards.pendingRuntimeNotice = notice + additionalFeedback
	e.emitProgress(ctx, "sources", state.CurrentRound, sessionID)
	common.PipelineInfo(ctx, "Agent", "source_read_handoff", map[string]any{
		"iteration": state.CurrentRound, "pending_actions": len(actions),
	})
	return true
}

func (e *AgentEngine) comparisonReadHandoff(ctx context.Context, state *types.AgentState, target taskCoverageTarget, gap coverageCompletion, record coverageRecord, read bool, draft string) map[string]any {
	if gap.NextRead == "" && gap.Reason != "comparison_target_incomplete" {
		return nil
	}
	readings, aligned := e.tableReceiptReadings(target, record, draft)
	if aligned {
		return nil
	}
	from, to, grain := coverageTargetDateRange(target)
	action := map[string]any{"goal_index": target.GoalIndex, "sources": gap.OpenItems,
		"table": target.Table, "metric": target.Metric, "requested_from": from, "requested_to": to}
	record.Evidence = slices.DeleteFunc(slices.Clone(record.Evidence), func(item coverageEvidence) bool {
		return !item.Bound || !coverageCurrentEvidence(record, item) || !e.coverageEvidenceAllowed(ctx, &target, item)
	})
	_, _, inspectAvailable := e.toolRegistry.StructuredInvocation("structured_inspect_source", json.RawMessage(`{}`))
	if grain == "month" && inspectAvailable {
		var cells []map[string]string
		for _, item := range record.Evidence {
			if item.SourceDomain != "structured" || item.Kind != "cell" || item.Sheet != target.Table || item.Date != to ||
				item.DateCell == "" || item.Grain == grain || comparisonNativeRowPeriod(item.Period, from, to, grain) ||
				slices.ContainsFunc(record.Evidence, func(inspected coverageEvidence) bool {
					return inspected.Operation == "inspect_source" && inspected.Kind == "cell" && inspected.Cell == item.DateCell &&
						inspected.Sheet == item.Sheet && comparisonPhysicalSource(inspected) == comparisonPhysicalSource(item)
				}) {
				continue
			}
			// Coordinates corroborate the model's planned period and a real row.
			// They leave field and source choice with the model.
			cell := map[string]string{"dataset_id": item.DatasetID, "sheet": item.Sheet, "date_cell": item.DateCell}
			if !slices.ContainsFunc(cells, func(prior map[string]string) bool {
				return prior["dataset_id"] == cell["dataset_id"] && prior["sheet"] == cell["sheet"] && prior["date_cell"] == cell["date_cell"]
			}) {
				cells = append(cells, cell)
			}
		}
		if len(cells) != 0 {
			action["workbook_date_cells"] = cells
		}
	}
	if read {
		docs, pages := e.comparisonDocumentReads(state, target, record)
		if len(pages) != 0 {
			action["action"], action["unread_document_pages"] = "read_comparison_table", pages
			return action
		}
		if len(docs) != 0 {
			action["action"], action["document_source_candidates"] = "locate_comparison_table", docs
			return action
		}
	}
	if action["workbook_date_cells"] != nil {
		action["action"] = "verify_workbook_row_period"
		return action
	}
	if from == "" || to == "" || target.Metric == "" || slices.ContainsFunc(readings, func(fact coverageFact) bool {
		return fact.Evidence.SourceDomain == "structured"
	}) || !e.toolRegistry.HasStructuredDataTools() {
		return nil
	}
	var fields []comparisonWorkbookField
	for _, item := range record.Evidence {
		if item.Kind != "directory" || item.Operation != "list_series" || item.Sheet != target.Table || item.Metric != target.Metric ||
			item.DatasetID == "" || item.SeriesID == "" ||
			slices.ContainsFunc(record.Evidence, func(read coverageEvidence) bool {
				return (read.Kind == "cell" || read.Kind == "gap") && read.DatasetID == item.DatasetID &&
					read.SeriesID == item.SeriesID && read.PeriodTo == to && (read.PeriodFrom == from || from <= read.Date && read.Date <= to)
			}) {
			continue
		}
		field := comparisonWorkbookField{item.DatasetID, item.SeriesID}
		if !slices.Contains(fields, field) {
			fields = append(fields, field)
		}
	}
	if len(fields) != 0 {
		action["action"], action["workbook_field_candidates"] = "read_comparison_field", fields[:min(len(fields), 8)]
		return action
	}
	return nil
}

type comparisonWorkbookField struct {
	Dataset string `json:"dataset_id"`
	Series  string `json:"series_id"`
}

type comparisonDocumentPage struct {
	Source string `json:"document_id"`
	Page   int    `json:"page"`
}

func (e *AgentEngine) comparisonDocumentReads(state *types.AgentState, target taskCoverageTarget, record coverageRecord) ([]string, []comparisonDocumentPage) {
	var docs []string
	for _, item := range record.Evidence {
		if item.SourceDomain == "knowledge" && item.SourceID != "" &&
			slices.Contains([]string{agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument, agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments}, item.Operation) &&
			!slices.Contains(docs, item.SourceID) {
			docs = append(docs, item.SourceID)
		}
	}
	var pages []comparisonDocumentPage
	located := make(map[string]bool)
	currentSteps := state.RoundSteps[min(e.evidenceCompletion.startStep, len(state.RoundSteps)):]
	for _, call := range e.evidenceCompletion.documentReads {
		if call.Name != agenttools.ToolReadDocument || call.Target != nil || call.Result == nil || call.Result.Data["execution_started"] == false {
			continue
		}
		data := call.Result.Data
		id := sourceNavigationString(data, "knowledge_id")
		if !slices.Contains(docs, id) {
			continue
		}
		var candidates []struct {
			Title string `json:"title"`
			Page  int    `json:"page"`
		}
		raw, _ := json.Marshal(data["table_candidates"])
		_ = json.Unmarshal(raw, &candidates)
		for _, candidate := range candidates {
			if candidate.Title != target.Table || candidate.Page < 1 {
				continue
			}
			located[id] = true
			page := comparisonDocumentPage{id, candidate.Page}
			if !slices.Contains(pages, page) && !comparisonDocumentPageAttempted(currentSteps, page) {
				pages = append(pages, page)
			}
		}
	}
	docs = slices.DeleteFunc(docs, func(id string) bool {
		if located[id] {
			return true
		}
		return slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
			return item.SourceDomain == "knowledge" && item.SourceID == id && item.Kind == "body" && item.TableGrid != nil && item.Sheet == target.Table
		})
	})
	slices.Sort(docs)
	slices.SortFunc(pages, func(a, b comparisonDocumentPage) int {
		if a.Source == b.Source {
			return a.Page - b.Page
		}
		return slices.Compare([]string{a.Source}, []string{b.Source})
	})
	return docs[:min(len(docs), 8)], pages[:min(len(pages), 8)]
}

func comparisonDocumentPageAttempted(steps []types.AgentStep, page comparisonDocumentPage) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Name != agenttools.ToolReadDocument || call.Target != nil || call.Result == nil ||
				call.Result.PreflightRejected || call.Result.Data["execution_started"] == false {
				continue
			}
			var args agenttools.ReadDocumentInput
			raw, err := json.Marshal(call.ExecutionArgs())
			if err != nil || json.Unmarshal(raw, &args) != nil || args.Query != "" || args.ImageMode == "pixels" || len(args.Images) != 0 {
				continue
			}
			if sourceNavigationString(call.Result.Data, "knowledge_id") != page.Source && args.ID != page.Source {
				continue
			}
			if args.Page == page.Page || slices.Contains(args.Pages, page.Page) {
				return true
			}
		}
	}
	return false
}

// restoreStructuredReviewConversation removes only the exact synthetic review
// envelope recorded by the runtime. The original human transcript is restored
// and the live tail is preserved; a changed/missing envelope leaves the live
// transcript untouched. Always return a fresh slice so callers cannot mutate
// the input through append.
func restoreStructuredReviewConversation(messages []chat.Message, guards *loopGuards) ([]chat.Message, bool) {
	if guards == nil || len(guards.structuredReviewEvidence) == 0 || guards.structuredReviewInput == "" || len(messages) < 2 {
		return append([]chat.Message(nil), messages...), false
	}
	baseSystem := ""
	if guards.structuredReviewEvidence[0].Role == "system" {
		baseSystem = guards.structuredReviewEvidence[0].Content
	}
	expectedReviewSystem := structuredReviewSystemContent(baseSystem)
	for i := 0; i+1 < len(messages); i++ {
		if messages[i].Role != "system" || messages[i].Content != expectedReviewSystem ||
			messages[i+1].Role != "user" || messages[i+1].Content != guards.structuredReviewInput {
			continue
		}

		// Keep any live prelude (including trusted system messages). Avoid
		// duplicating the saved transcript's leading system message when the
		// prelude already contains that exact message.
		prefix := append([]chat.Message(nil), messages[:i]...)
		evidence := append([]chat.Message(nil), guards.structuredReviewEvidence...)
		if len(evidence) > 0 && evidence[0].Role == "system" {
			for _, message := range prefix {
				if message.Role == evidence[0].Role && message.Content == evidence[0].Content &&
					message.Name == evidence[0].Name && message.ToolCallID == evidence[0].ToolCallID {
					evidence = evidence[1:]
					break
				}
			}
		}
		restored := make([]chat.Message, 0, len(prefix)+len(evidence)+len(messages)-i-2)
		restored = append(restored, prefix...)
		restored = append(restored, evidence...)
		restored = append(restored, messages[i+2:]...)
		return restored, true
	}
	return append([]chat.Message(nil), messages...), false
}

const evidenceRecoveryInstructions = `内部运行状态：审核提出了待核实的证据缺口，现返回证据收集阶段。先核对已有成功结果及已补读片段；审核意见不代表原证据缺失或失效。后续失败仅约束该次请求，不抹去先前成功返回，仍按实际来源范围判断。根据完整用户请求、真实来源及下面的核验意见，自行决定最小必要的读取或计算计划并调用实际工具；不要仅把用户必需的缺失结论删掉就宣布完成，也不要把核验意见当成来源事实。若缺口只属于自行附加的换算、解释或范围断言，而用户必需的主结果与交付已有证据，优先重写候选省去这些可选内容，再接受完整核验；不得把用户要求的图表、文件、核心数值或口径降为可选内容。
已有证据继续复用。结果被截断、分页未读完或仅有目录/检索摘要时，利用真实来源ID、页码、范围和续读位置定向补读；多来源任务分别核实必要来源，不用一个文件的片段替其他文件。读取顺序、口径和工具由你根据任务决定，权限、来源绑定和确定性计算规则仍适用。新取得证据后重新形成完整候选并接受核验。真正无法取得时说明已尝试的限制和可回答范围，不编造原值、不声称全部完成。` + "\n" + structuredAvailabilityInstructions

const numericValidationRepairInstructions = `内部运行状态：确定性数值校验指出当前候选含有尚未匹配到真实程序结果的数字。以下校验记录只是运行时反馈，不是来源事实。依据完整用户请求、真实工具结果和该反馈修订：保留用户必答内容；可以删除非必要且无依据的附加数字，也可以自行选择已授权的读取或计算工具补足依据。不要把校验反馈当作来源，不要编造、改写或放宽数值规则。未匹配token是当前答复中的数字而非工具ref；不能把它或任何原数值填入calculate_source_values的ref。该工具仅接受对真实已读文档chunk执行inspect签发的ref；结构化查询结果的派生计算应使用同一授权数据源的查询/分析能力，并显式返回所需计算结果。区间端点须采用实际证据或执行的统计结果；不得把原值整圆后作为已验证端点。` + numericResultPresentationInstructions + `修改后的候选仍会再次接受来源核验和确定性数值校验。`

const finalNumericRepairInstructions = `这是本轮最后一次数值修订。按照完整用户请求，只交付已经验证的必答结果和必要来源引用；省略非必要的附加定义、页码、表号、延伸历史数字和解释。不得省略用户要求的差值、比例、比较或其他必答内容；必答数字尚无依据时，仍须调用已授权工具取数或计算，不得靠删掉必答结果结束。先前因无依据删除的附加数字，未取得新依据不要恢复。不得改写成中文数字或其他形式绕过校验。数值门槛、来源复核和剩余执行预算均不变。`

// resumeNumericValidationRepair restores the exact pre-review transcript and
// resumes the ordinary authorized tool loop. Numeric repairs have their own
// caller-owned bound and do not consume or reset source-recovery attempts.
func (e *AgentEngine) resumeNumericValidationRepair(ctx context.Context, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID, candidate string, feedback map[string]any) {
	if guards.structuredEvidenceRepairs >= 2 {
		e.turnReview.NumericWarningAllowed = true
	}
	restored, restoredOriginal := restoreStructuredReviewConversation(*messages, guards)
	if len(guards.numericValidationInputs) > 0 {
		prior := make(map[string]bool, len(guards.numericValidationInputs))
		for _, input := range guards.numericValidationInputs {
			prior[input] = true
		}
		filtered := make([]chat.Message, 0, len(restored))
		for _, message := range restored {
			if message.Role == "system" && prior[message.Content] {
				continue
			}
			filtered = append(filtered, message)
		}
		restored = filtered
	}
	*messages = restored
	encodedFeedback, err := json.Marshal(feedback)
	if err != nil {
		// The builder emits JSON-safe protocol values. Keep a fail-closed
		// representation if a future field violates that contract.
		encodedFeedback = []byte(`{"status":"feedback_encoding_failed"}`)
	}
	feedbackMessage := numericValidationRepairInstructions + "\n数值校验记录：" + string(encodedFeedback)
	feedbackMessage += e.repeatedNumericRepairAdvice()
	if guards.structuredEvidenceRepairs >= 2 {
		feedbackMessage += "\n" + finalNumericRepairInstructions
	}
	guards.numericValidationInputs = append(guards.numericValidationInputs, feedbackMessage)
	*messages = append(*messages, chat.Message{Role: "assistant", Content: candidate}, chat.Message{Role: "system", Content: feedbackMessage})
	e.structuredReviewMode = false
	e.clearTaskToolProjection(messages)
	guards.sawPlainRound = false
	guards.consecutiveSameContent = 0
	e.contextRewrites++
	e.lastUsage = types.TokenUsage{}
	e.lastSentMsgCount = 0
	e.emitProgress(ctx, "sources", state.CurrentRound, sessionID)
	common.PipelineInfo(ctx, "Agent", "numeric_validation_recovery", map[string]interface{}{
		"iteration": state.CurrentRound, "attempt": guards.structuredEvidenceRepairs,
		"phase": "numeric_candidate_repair", "original_transcript_restored": restoredOriginal,
	})
}

// Shared by ordinary recovery and final synthesis; this changes feedback only,
// never the existing numeric verdict, source evidence, or candidate repair budget.
func (e *AgentEngine) repeatedNumericRepairAdvice() string {
	repeated := e.turnReview.Last.RepeatedUnsupported
	if len(repeated) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(repeated[:min(len(repeated), numericValidationFeedbackIssueLimit)])
	return "\n上次修订后仍未匹配的数字：" + string(encoded) + "。这些数字仍出现在当前候选，上一轮改写没有解决它们。逐项定位当前上下文并修正对应断言；其他已核实内容和用户要求的简析、图件必须保留。不得原样保留未获依据的区间端点或自行整圆数值。"
}
