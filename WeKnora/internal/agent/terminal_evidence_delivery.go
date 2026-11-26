package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const terminalCoveragePreviewLimit = 5

// Only fixed runtime fallbacks reach this path. The text reports observations
// at their executed scope without treating them as a completed task.
func (e *AgentEngine) terminalEvidenceCompletionDelivery(ctx context.Context, state *types.AgentState) string {
	if !e.evidenceCompletionEligible() || !e.evidenceCompletionEnabled() || state == nil || e.holdCollaborationAnswer {
		return ""
	}
	hadCoverage := e.hasEvidenceCompletionContext()
	e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, e.turnReview.Conversation)
	turn := &e.evidenceCompletion
	record := coverageRecord{Revision: turn.revision, AuthScope: turn.scope, Snapshots: make(map[string]string)}
	type locator struct{ evidence, library, source, pointer string }
	locate := func(item coverageEvidence) locator {
		pointer := item.JSONPointer
		// A verified inspected boundary refers to a field's metadata within
		// the directory row rather than to a standalone native observation.
		if item.Kind == "gap" && item.LastValidRef != "" {
			pointer = strings.TrimSuffix(pointer, "/last_valid_date")
		}
		return locator{item.EvidenceID, item.KnowledgeBaseID, item.SourceID, pointer}
	}
	observed := make(map[locator]bool)
	seen := make(map[string]bool)
	catalogs := make(map[string]map[string]any)
	var inspected, rejected, failed bool
	// Reproject finished calls to check the payload and snapshot at publication,
	// including the most recent step and any replacement of a recorded result.
	for _, step := range state.RoundSteps[min(turn.startStep, len(state.RoundSteps)):] {
		batch := e.taskCoverageBatch(step, state, seen)
		inspected = inspected || len(batch.Attempts) != 0
		attempts := make(map[string]coverageAttempt, len(batch.Attempts))
		for _, attempt := range batch.Attempts {
			attempts[attempt.ToolCallID] = attempt
		}
		for _, call := range step.ToolCalls {
			attempt, ok := attempts[call.ID]
			if call.Result == nil || !ok {
				continue
			}
			if attempt.Success && attempt.Operation == "list_datasets" {
				catalogs[attempt.EvidenceID] = call.Result.Data
			}
			blocked := call.Result.PreflightRejected || call.Result.Data["execution_started"] == false
			rejected = rejected || blocked
			failed = failed || !blocked && !call.Result.Success
		}
		for _, observation := range batch.Evidence {
			observation.Revision, observation.AuthScope = record.Revision, record.AuthScope
			if observation.Bound {
				observed[locate(observation)] = true
				if observation.SnapshotHash != "" {
					record.Snapshots[coverageSnapshotKey(observation)] = observation.SnapshotHash
				}
			}
			record.Evidence = append(record.Evidence, observation)
		}
	}
	targets := e.evidenceCompletionTargets()
	hasTarget := slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
		return target.SourceTime != "general_concept" && !slices.Equal(target.Required, []string{"optional"})
	})
	if !inspected && !hadCoverage && !hasTarget {
		return "" // No current source task or source activity.
	}

	chinese := strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")
	var facts, directory, samples, documents []string
	factLines := make(map[string]bool)
	var hasValue, hasGap bool
	current := func(item coverageEvidence) bool {
		return observed[locate(item)] && item.Bound && coverageCurrentEvidence(record, item) && e.terminalCoverageScopeAllowed(targets, item)
	}
	addFact := func(item coverageEvidence) {
		if line := terminalCoverageFact(item, chinese); line != "" && !factLines[line] {
			factLines[line] = true
			facts = append(facts, line)
			hasValue = hasValue || item.Kind != "gap"
			hasGap = hasGap || item.Kind == "gap"
		}
	}
	for _, result := range turn.result {
		for _, fact := range result.PinnedFacts {
			item := fact.Evidence
			if fact.Role != "last_valid_date" && item.Kind != "directory" && current(item) {
				addFact(item) // The value entry carries its paired date and address.
			}
		}
	}
	// The original model chose the native reads' fields and parameters. Exact
	// lookups and latest reads can be shown as independently observed facts.
	var gaps []coverageEvidence
	for _, item := range record.Evidence {
		if !current(item) {
			continue
		}
		switch {
		case len(directory) < terminalCoveragePreviewLimit && (item.Kind == "directory" || item.Kind == "navigation"):
			if line := terminalCoverageDirectory(item, catalogs[item.EvidenceID], chinese); line != "" && !slices.Contains(directory, line) {
				directory = append(directory, line)
			}
		case item.Kind == "gap":
			gaps = append(gaps, item)
		case item.Operation == "query_series" && item.Kind == "cell" &&
			(item.LatestInScope || item.Date != "" && item.Date == item.PeriodFrom && item.Date == item.PeriodTo):
			addFact(item)
		case !hasValue && len(samples) < terminalCoveragePreviewLimit && item.Kind == "cell" &&
			(item.Operation == "query_series" || item.Operation == "inspect_source"):
			if line := terminalCoverageFact(item, chinese); line != "" && !slices.Contains(samples, line) {
				samples = append(samples, line)
			}
		case !hasValue && len(documents) < terminalCoveragePreviewLimit && item.Kind == "body" && item.Complete:
			if line := terminalCoverageDocumentRead(item, chinese); line != "" && !slices.Contains(documents, line) {
				documents = append(documents, line)
			}
		}
	}
	for _, gap := range gaps {
		addFact(gap)
	}

	lines := facts
	status, rejectedNotice, failedNotice := "The evidence above covers the listed sources and scope.",
		"Some source requests stopped before a read began.", "Some source queries failed to return a usable result."
	if chinese {
		status, rejectedNotice, failedNotice = "以上是本轮核实到的来源和范围。", "部分来源请求在读取开始前被拦下。", "部分来源查询未返回可用结果。"
	}
	if hasGap {
		status = "The listed gaps still lack a verifiable observation."
		if chinese {
			status = "上述缺数范围仍缺可核实的观测值。"
		}
	}
	if !hasValue && len(samples) > 0 {
		// A bounded preview of physically read cells can demonstrate what was
		// actually obtained, without presenting it as the requested scope.
		lines = append(lines, samples...)
		sampleNotice := "The original cells shown are from this turn's reads."
		if chinese {
			sampleNotice = "所列原格来自本轮实读。"
		}
		if hasGap {
			status = sampleNotice + " " + status
		} else {
			status = sampleNotice
		}
	}
	if !hasValue && len(documents) > 0 {
		lines = append(lines, documents...)
	}
	if gap, open := e.evidenceCompletionGap(); open && slices.Contains(gap.OpenItems, "attributed_supplement") && len(lines) > 0 {
		status = "The requested supplemental source evidence is still unverified."
		if chinese {
			status = "所需的补充来源证据仍未核实。"
		}
	} else if len(lines) == 0 && len(directory) > 0 {
		lines = directory
		status = "The catalog was available. No original observation was verified for the requested result."
		if chinese {
			status = "本轮核实到上述目录范围，所求原始观测值仍待核实。"
		}
	} else if len(lines) == 0 {
		status = "This turn returned no verifiable source observation. The requested result remains open."
		if chinese {
			status = "本轮未取得可核实的来源观测，所求结论仍待核实。"
		}
	}
	if len(lines) > 0 {
		status = "- " + strings.Join(lines, "\n- ") + "\n\n" + status
	}
	if rejected {
		status += " " + rejectedNotice
	}
	if failed {
		status += " " + failedNotice
	}
	return status
}

func terminalCoverageDocumentRead(item coverageEvidence, chinese bool) string {
	if item.SourceTitle == "" {
		return ""
	}
	title := terminalEvidenceText(item.SourceTitle)
	if item.Page > 0 {
		if chinese {
			return fmt.Sprintf("本轮已读取 %s 第 %d 页返回的正文。", title, item.Page)
		}
		return fmt.Sprintf("This turn read the returned text from page %d of %s.", item.Page, title)
	}
	if chinese {
		return "本轮已读取 " + title + " 返回的正文片段。"
	}
	return "This turn read a returned text passage from " + title + "."
}

func (e *AgentEngine) terminalCoverageScopeAllowed(targets []taskCoverageTarget, item coverageEvidence) bool {
	source := item.SourceID
	if item.SourceDomain == "structured" {
		source = "" // The native structured executor authorizes whole libraries.
	}
	if !e.coverageScopeAllows(item.KnowledgeBaseID, source) {
		return false
	}
	if domain := e.coverageOnlyDomain(); domain != "" && domain != item.SourceDomain {
		return false
	}
	if !slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" }) {
		return true
	}
	for _, target := range targets {
		docs, explicit := e.coverageOnlySelectedDocuments(target)
		if explicit && !coverageSelectedDocumentMatches(docs, item) {
			continue
		}
		if explicit || slices.ContainsFunc(e.evidenceCompletion.record.Bindings, func(binding coverageBinding) bool {
			return binding.GoalIndex == target.GoalIndex && (target.SourceMode != "only" || binding.Role == "primary") &&
				binding.SourceID == item.SourceID && binding.KnowledgeBaseID == item.KnowledgeBaseID
		}) {
			return true
		}
	}
	return false
}

var terminalEvidenceEscaper = strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~")

func terminalEvidenceText(value string) string {
	return terminalEvidenceEscaper.Replace(html.EscapeString(strings.Join(strings.Fields(value), " ")))
}

func terminalCoverageLabel(item coverageEvidence) string {
	var fields []string
	for _, value := range []string{item.SourceTitle, item.Sheet, item.Subject, item.Metric} {
		if value != "" && !slices.Contains(fields, terminalEvidenceText(value)) {
			fields = append(fields, terminalEvidenceText(value))
		}
	}
	return strings.Join(fields, " / ")
}

func terminalCoverageDirectory(item coverageEvidence, catalog map[string]any, chinese bool) string {
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	directoryLine := func(label, date string) string {
		if date != "" {
			if chinese {
				return label + "，目录所列记录截至 " + terminalEvidenceText(date) + "。"
			}
			return label + " is listed with records through " + terminalEvidenceText(date) + "."
		}
		if chinese {
			return label + " 出现在本轮来源目录中。"
		}
		return label + " appeared in this turn's source listings."
	}
	var scope []string
	for _, field := range sourceNavigationRows(catalog["literal_fields"]) {
		if len(scope) >= terminalCoveragePreviewLimit {
			break
		}
		sheet, metric := sourceNavigationString(field, "sheet"), sourceNavigationString(field, "metric")
		if field["dataset_id"] != item.DatasetID || sheet == "" || metric == "" {
			continue
		}
		candidate := item
		candidate.Sheet, candidate.Metric = sheet, metric
		if line := directoryLine(terminalCoverageLabel(candidate), sourceNavigationString(field, "last_date")); !slices.Contains(scope, line) {
			scope = append(scope, line)
		}
	}
	if len(scope) > 0 {
		return strings.Join(scope, " ")
	}
	if entries, ok := coverageObject(item.Raw)["sheets"].([]any); ok && len(entries) == 1 {
		if sheet, ok := entries[0].(string); ok && sheet != "" {
			label += " / " + terminalEvidenceText(sheet)
		}
	}
	return directoryLine(label, item.LastObservedDate)
}

func terminalCoverageFact(item coverageEvidence, chinese bool) string {
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	if item.Kind == "gap" && item.Complete && coverageExactRange(item) {
		period := firstNonEmptyAgent(item.Period, item.PeriodFrom)
		if item.PeriodFrom != "" && item.PeriodFrom != item.PeriodTo {
			period += " – " + item.PeriodTo
		}
		if chinese {
			return fmt.Sprintf("%s 在已核查范围 %s 内没有有效观测值。", label, terminalEvidenceText(period))
		}
		return fmt.Sprintf("%s returned no valid observation in the checked scope %s.", label, terminalEvidenceText(period))
	}
	var raw string
	switch value := item.Raw.(type) {
	case string:
		raw = terminalEvidenceText(value)
	case json.Number:
		raw = value.String()
	}
	if raw == "" {
		return ""
	}
	switch item.Kind {
	case "cell", "last_valid", "body", "derived":
	default:
		return ""
	}
	dateCell, valueCell, page, unknownUnit := "date cell ", "value cell ", "page %d", "unit unconfirmed"
	original, latest, sourceResult := "Recorded value", "Latest valid observation in the checked scope", "Verified source result"
	if chinese {
		dateCell, valueCell, page, unknownUnit = "日期格 ", "取值格 ", "第 %d 页", "单位未确认"
		original, latest, sourceResult = "原值", "已核查范围内的最近有效值", "已核对的来源结果"
	}
	var details []string
	if date := firstNonEmptyAgent(item.Date, item.Period); date != "" {
		details = append(details, terminalEvidenceText(date))
	}
	if item.DateCell != "" {
		details = append(details, dateCell+terminalEvidenceText(item.DateCell))
	}
	if item.Cell != "" {
		details = append(details, valueCell+terminalEvidenceText(item.Cell))
	}
	if item.Page > 0 {
		details = append(details, fmt.Sprintf(page, item.Page))
	}
	descriptor := original
	if item.LatestInScope || item.Kind == "last_valid" {
		descriptor = latest
	} else if item.Kind == "derived" || item.Kind == "body" {
		descriptor = sourceResult
	}
	if item.Unit != "" && item.UnitBasis != "" && item.UnitBasis != "none" && item.UnitBasis != "needs_review" {
		raw += " " + terminalEvidenceText(item.Unit)
	} else if item.Kind == "cell" || item.Kind == "last_valid" {
		details = append(details, unknownUnit)
	}
	if len(details) > 0 {
		raw += " (" + strings.Join(details, ", ") + ")"
	}
	if chinese {
		return label + "，" + descriptor + " " + raw + "。"
	}
	return label + ". " + descriptor + " " + raw + "."
}
