package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const coverageScopeRejection = "source call is outside the verified task source scope"

type coverageSourceIdentity struct {
	domain, library, id, knowledge, dataset, object string
}

func coverageSourceOf(item coverageEvidence) coverageSourceIdentity {
	knowledge := item.KnowledgeID
	if item.SourceDomain == "knowledge" {
		knowledge = item.SourceID
	}
	return coverageSourceIdentity{
		domain: item.SourceDomain, library: item.KnowledgeBaseID, id: item.SourceID,
		knowledge: knowledge, dataset: item.DatasetID, object: item.Object,
	}
}

type verifiedCoverageScope struct {
	domain  string
	pinned  bool
	sources []coverageSourceIdentity
}

func (s verifiedCoverageScope) contains(item coverageEvidence) bool {
	source := coverageSourceOf(item)
	return slices.ContainsFunc(s.sources, func(allowed coverageSourceIdentity) bool {
		return allowed.library == source.library &&
			(allowed.domain == source.domain && allowed.id == source.id ||
				allowed.knowledge == source.knowledge && source.knowledge != "" &&
					(allowed.domain == "knowledge" || allowed.domain == source.domain && allowed.dataset == ""))
	})
}

// Task scope only narrows native authorization. The structured store grants
// whole libraries, while the document tools grant actual document identities.
func (e *AgentEngine) coverageEvidenceAuthorized(ctx context.Context, item coverageEvidence) bool {
	switch item.SourceDomain {
	case "knowledge":
		return item.SourceID != "" && e.coverageScopeAllows(item.KnowledgeBaseID, item.SourceID) &&
			types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, item.KnowledgeBaseID) == nil
	case "structured":
		return item.SourceID != "" && e.coverageScopeAllows(item.KnowledgeBaseID, "") &&
			types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, item.KnowledgeBaseID) == nil
	default:
		return true // Artifacts keep their own native source checks.
	}
}

func (e *AgentEngine) restrictedCoverageSourceAuthorized(ctx context.Context, item coverageEvidence) bool {
	if e.config == nil || !e.coverageEvidenceAuthorized(ctx, item) {
		return false
	}
	source := item.SourceID
	if item.SourceDomain == "structured" {
		source = ""
	}
	return agenttools.SearchTargetsIncludeSource(e.config.SearchTargets, item.KnowledgeBaseID, source)
}

func (e *AgentEngine) verifiedCoverageScopePins(ctx context.Context, target taskCoverageTarget) verifiedCoverageScope {
	var scope verifiedCoverageScope
	if target.SourceMode != "only" {
		return scope
	}
	scope.domain = e.coveragePlanPrimaryDomain()
	compare := target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions"
	if compare {
		scope.domain = ""
	}
	if e.modelContext == nil {
		return scope
	}
	var chosen []*SelectedDocumentInfo
	var single bool
	for _, doc := range e.selectedDocs {
		if doc == nil || doc.KnowledgeID == "" || doc.KnowledgeBaseID == "" {
			continue
		}
		ref := e.modelContext.RegisterDocument(doc.KnowledgeID)
		if (ref == target.PrimaryRef || ref == target.RestrictionBasis) && (!compare || len(target.ComparisonSources) == 0) {
			chosen = []*SelectedDocumentInfo{doc}
			single = true
			break
		}
		if slices.Contains(target.ComparisonSources, ref) || compare && len(target.ComparisonSources) == 0 {
			chosen = append(chosen, doc)
		}
	}
	if len(chosen) == 0 {
		return scope
	}
	// Explicit handles retain their restriction even when access is revoked.
	// In a mixed-domain comparison, consider each independently named side.
	scope.pinned = single || !compare || len(target.ComparisonSources) == 0 || len(chosen) == len(target.ComparisonSources)
	if !compare {
		scope.domain = "knowledge"
	}
	for _, doc := range chosen {
		item := coverageEvidence{SourceDomain: "knowledge", SourceID: doc.KnowledgeID, KnowledgeBaseID: doc.KnowledgeBaseID}
		if e.restrictedCoverageSourceAuthorized(ctx, item) {
			scope.sources = append(scope.sources, coverageSourceOf(item))
		}
	}
	return scope
}

func (e *AgentEngine) verifiedCoverageSourceScope(ctx context.Context, target taskCoverageTarget, record coverageRecord) verifiedCoverageScope {
	scope := e.verifiedCoverageScopePins(ctx, target)
	if target.SourceMode != "only" {
		return scope
	}
	if chosen := e.evidenceCompletion.nativeSelections[target.GoalIndex]; len(chosen) > 0 {
		scope.pinned = true
		for _, item := range chosen {
			if e.restrictedCoverageSourceAuthorized(ctx, item) {
				scope.sources = append(scope.sources, coverageSourceOf(item))
			}
		}
	}
	for _, binding := range record.Bindings {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" {
			continue
		}
		for _, observed := range record.Evidence {
			if binding.KnowledgeBaseID != observed.KnowledgeBaseID || binding.SourceID != observed.SourceID ||
				binding.SnapshotHash != "" && binding.SnapshotHash != observed.SnapshotHash ||
				binding.EvidenceID != "" && binding.EvidenceID != observed.EvidenceID ||
				binding.JSONPointer != "" && binding.JSONPointer != observed.JSONPointer ||
				scope.domain != "" && scope.domain != observed.SourceDomain ||
				!coverageCurrentEvidence(record, observed) || !e.restrictedCoverageSourceAuthorized(ctx, observed) ||
				scope.pinned && !scope.contains(observed) {
				continue
			}
			source := coverageSourceOf(observed)
			if !slices.Contains(scope.sources, source) {
				scope.sources = append(scope.sources, source)
			}
		}
	}
	return scope
}

func (e *AgentEngine) recordNativeCoverageSelections(step types.AgentStep, observations []coverageEvidence) {
	turn := &e.evidenceCompletion
	for index, call := range step.ToolCalls {
		if call.CoverageSelectionGoal == nil || call.Result == nil || !call.Result.Success {
			continue
		}
		goal := *call.CoverageSelectionGoal
		if len(turn.nativeSelections[goal]) != 0 || slices.ContainsFunc(slices.Concat(step.ToolCalls[:index], step.ToolCalls[index+1:]), func(other types.ToolCall) bool {
			return other.CoverageSelectionGoal != nil && *other.CoverageSelectionGoal == goal &&
				other.Result != nil && other.Result.Success
		}) {
			continue // A parallel batch with competing choices does not select an object.
		}
		for _, item := range observations {
			if item.ToolCallID != call.ID || item.Kind != "directory" || !item.Bound {
				continue
			}
			if turn.nativeSelections == nil {
				turn.nativeSelections = make(map[int][]coverageEvidence)
			}
			prior := turn.nativeSelections[goal]
			if !slices.ContainsFunc(prior, func(existing coverageEvidence) bool {
				return existing.SourceDomain == item.SourceDomain && existing.KnowledgeBaseID == item.KnowledgeBaseID && existing.SourceID == item.SourceID
			}) {
				turn.nativeSelections[goal] = append(prior, item)
			}
		}
	}
}

func (e *AgentEngine) coverageEvidenceAllowed(ctx context.Context, target *taskCoverageTarget, item coverageEvidence) bool {
	if !e.coverageEvidenceAuthorized(ctx, item) {
		return false
	}
	record := e.currentCoverageScopeRecord(ctx)
	if target != nil {
		return target.SourceMode != "only" || e.verifiedCoverageSourceScope(ctx, *target, record).contains(item)
	}
	targets := e.evidenceCompletionTargets()
	if !slices.ContainsFunc(targets, func(goal taskCoverageTarget) bool { return goal.SourceMode == "only" }) {
		return true
	}
	for _, goal := range targets {
		if goal.SourceMode == "only" {
			if e.verifiedCoverageSourceScope(ctx, goal, record).contains(item) {
				return true
			}
			continue
		}
		if slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
			return binding.GoalIndex == goal.GoalIndex && binding.KnowledgeBaseID == item.KnowledgeBaseID &&
				binding.SourceID == item.SourceID && (binding.SnapshotHash == "" || binding.SnapshotHash == item.SnapshotHash)
		}) {
			return true
		}
	}
	return false
}

func (e *AgentEngine) coverageOnlyDomain() string {
	if domain := e.coveragePlanPrimaryDomain(); domain != "" && len(e.evidenceCompletionTargets()) > 0 &&
		!slices.ContainsFunc(e.evidenceCompletionTargets(), func(goal taskCoverageTarget) bool {
			return goal.SourceMode != "only" || goal.VersionTask == "compare_sources" || goal.VersionTask == "compare_versions"
		}) {
		return domain
	}
	return ""
}

func (e *AgentEngine) currentCoverageScopeRecord(ctx context.Context) coverageRecord {
	turn := &e.evidenceCompletion
	if turn.scope != "" && turn.scope != e.unverifiedSourceScopeDigest(ctx) {
		return coverageRecord{}
	}
	return turn.record
}

func (e *AgentEngine) coverageCallTargets(call types.LLMToolCall) []taskCoverageTarget {
	targets := e.evidenceCompletionTargets()
	if call.CoverageGoalIndex != nil {
		if index := slices.IndexFunc(targets, func(goal taskCoverageTarget) bool { return goal.GoalIndex == *call.CoverageGoalIndex }); index >= 0 {
			return targets[index : index+1]
		}
	}
	return targets
}

func (e *AgentEngine) coverageDocumentExecutionContext(ctx context.Context, call types.LLMToolCall, selecting *int) context.Context {
	if !e.coverageRegistrationEligible() {
		return ctx
	}
	switch call.Function.Name {
	case agenttools.ToolReadDocument, agenttools.ToolSearchKnowledge, agenttools.ToolBindDocumentScope:
	default:
		return ctx
	}
	targets := e.coverageCallTargets(call)
	if len(targets) == 0 || slices.ContainsFunc(targets, func(goal taskCoverageTarget) bool { return goal.SourceMode != "only" }) {
		return ctx
	}
	libraries := map[string]string{}
	add := func(id, library string) {
		if previous, exists := libraries[id]; exists && previous != library {
			library = "" // Conflicting directory identities cannot authorize either library.
		}
		libraries[id] = library
	}
	record := e.currentCoverageScopeRecord(ctx)
	if selecting != nil {
		for _, item := range record.Evidence {
			if item.SourceDomain == "knowledge" && item.Kind == "directory" && item.Bound &&
				coverageCurrentEvidence(record, item) && e.restrictedCoverageSourceAuthorized(ctx, item) {
				add(item.SourceID, item.KnowledgeBaseID)
			}
		}
	} else {
		for _, target := range targets {
			for _, source := range e.verifiedCoverageSourceScope(ctx, target, record).sources {
				if source.domain == "knowledge" {
					add(source.id, source.library)
				}
			}
		}
	}
	return agenttools.WithVerifiedDocumentLibraries(ctx, libraries)
}

// Native selection chooses an object using directory metadata without reading
// business values. An optional declaration never creates a source grant.
func (e *AgentEngine) coverageNativeSelectionGoal(ctx context.Context, call types.LLMToolCall, args map[string]any) *int {
	targets := e.coverageCallTargets(call)
	if len(targets) != 1 || targets[0].SourceMode != "only" {
		return nil
	}
	goal := targets[0]
	record := e.currentCoverageScopeRecord(ctx)
	scope := e.verifiedCoverageSourceScope(ctx, goal, record)
	if scope.pinned || len(scope.sources) != 0 || slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
		return binding.GoalIndex == goal.GoalIndex && binding.Role == "primary"
	}) {
		return nil
	}
	domain := e.coverageToolDomain(call.Function.Name, args)
	if scope.domain != "" && scope.domain != domain {
		return nil
	}
	known := func(match func(coverageEvidence) bool) bool {
		return slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
			return item.SourceDomain == domain && item.Kind == "directory" && item.Bound &&
				coverageCurrentEvidence(record, item) && e.restrictedCoverageSourceAuthorized(ctx, item) && match(item)
		})
	}
	if domain == "knowledge" && call.Function.Name == agenttools.ToolBindDocumentScope {
		var plan agenttools.DocumentScopePlan
		if json.Unmarshal([]byte(call.Function.Arguments), &plan) != nil {
			return nil
		}
		limit := 1
		if goal.VersionTask == "compare_sources" || goal.VersionTask == "compare_versions" {
			limit = 2
		}
		if len(plan.Sources) != limit {
			return nil
		}
		for _, source := range plan.Sources {
			if !known(func(item coverageEvidence) bool {
				metadata := coverageObject(item.Raw)
				return source.KnowledgeID == item.SourceID &&
					(source.FileName != "" && source.FileName == metadata["file_name"] || source.Title != "" && source.Title == metadata["title"])
			}) {
				return nil
			}
		}
		return &goal.GoalIndex
	}
	if domain == "structured" && goal.VersionTask != "compare_sources" && goal.VersionTask != "compare_versions" {
		op, inner, ok := e.toolRegistry.StructuredInvocation(call.Function.Name, json.RawMessage(call.Function.Arguments))
		library, dataset := e.coverageStructuredInvocationLibrary(args), sourceNavigationString(inner, "dataset_id")
		if ok && op.Operation == "list_series" && library != "" && dataset != "" && known(func(item coverageEvidence) bool {
			return item.Operation == "list_datasets" && item.KnowledgeBaseID == library && item.DatasetID == dataset
		}) {
			return &goal.GoalIndex
		}
	}
	return nil
}

// Selectors are taken from the arguments the native executor will receive.
// A separate optional goal association can narrow action scope in a mixed task.
func (e *AgentEngine) coverageSourceCallAllowed(ctx context.Context, call types.LLMToolCall, args map[string]any) bool {
	if !e.coverageRegistrationEligible() {
		return true
	}
	targets := e.coverageCallTargets(call)
	if len(targets) == 0 || slices.ContainsFunc(targets, func(goal taskCoverageTarget) bool { return goal.SourceMode != "only" }) {
		return true
	}
	domain := e.coverageToolDomain(call.Function.Name, args)
	if domain == "" {
		return !e.coverageUnpermittedSourceCall(call.Function.Name, args)
	}
	operation, inner := call.Function.Name, args
	structuredLibrary := ""
	if domain == "structured" {
		raw, _ := json.Marshal(args)
		if descriptor, actual, ok := e.toolRegistry.StructuredInvocation(call.Function.Name, raw); ok {
			operation, inner = descriptor.Operation, actual
		}
		structuredLibrary = e.coverageStructuredInvocationLibrary(args)
	}
	record := e.currentCoverageScopeRecord(ctx)
	var combinedDocumentScope verifiedCoverageScope
	for _, target := range targets {
		scope := e.verifiedCoverageSourceScope(ctx, target, record)
		if scope.domain != "" && scope.domain != domain {
			continue
		}
		if operation == agenttools.ToolSearchKnowledge || operation == agenttools.ToolBindDocumentScope || operation == agenttools.ToolReadDocument {
			combinedDocumentScope.sources = append(combinedDocumentScope.sources, scope.sources...)
			continue
		}
		if e.coverageScopePermitsInvocation(ctx, scope, domain, operation, structuredLibrary, inner) {
			return true
		}
	}
	if domain == "knowledge" && len(combinedDocumentScope.sources) > 0 {
		return e.coverageDocumentCallAllowed(combinedDocumentScope, call)
	}
	return false
}

func (e *AgentEngine) coverageStructuredInvocationLibrary(args map[string]any) string {
	if library := sourceNavigationString(args, "knowledge_base_id"); library != "" || e.config == nil {
		return library
	}
	var library string
	for _, target := range e.config.SearchTargets {
		if target == nil || !agenttools.SearchTargetsIncludeSource(e.config.SearchTargets, target.KnowledgeBaseID, "") {
			continue
		}
		if library != "" && library != target.KnowledgeBaseID {
			return ""
		}
		library = target.KnowledgeBaseID
	}
	return library
}

func (e *AgentEngine) coverageScopePermitsInvocation(ctx context.Context, scope verifiedCoverageScope, domain, operation, structuredLibrary string, args map[string]any) bool {
	switch operation {
	case agenttools.ToolListDocuments, "list_datasets", "list_pdf_tables":
		// A directory can locate the actual identity for a named selection.
		// Its entries are metadata and cannot satisfy a read or confer scope.
		library := firstNonEmptyAgent(structuredLibrary, sourceNavigationString(args, "knowledge_base_id"))
		return library == "" || types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, library) == nil &&
			(!scope.pinned || slices.ContainsFunc(scope.sources, func(source coverageSourceIdentity) bool { return source.library == library }))
	}
	if domain == "structured" {
		if structuredLibrary == "" || !e.coverageScopeAllows(structuredLibrary, "") ||
			types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, structuredLibrary) != nil {
			return false
		}
		dataset, object := sourceNavigationString(args, "dataset_id"), sourceNavigationString(args, "table_id")
		return slices.ContainsFunc(scope.sources, func(source coverageSourceIdentity) bool {
			if source.domain != domain || source.library != structuredLibrary {
				return false
			}
			if operation == "read_pdf_table" || operation == "query_pdf_table" {
				return object != "" && object == source.object && (dataset == "" || dataset == source.dataset)
			}
			return dataset != "" && dataset == firstNonEmptyAgent(source.dataset, source.id) && (object == "" || object == source.object)
		})
	}
	return false
}

func (e *AgentEngine) coverageDocumentCallAllowed(scope verifiedCoverageScope, call types.LLMToolCall) bool {
	raw := []byte(call.Function.Arguments)
	var ids []string
	var plan *agenttools.DocumentScopePlan
	switch call.Function.Name {
	case agenttools.ToolReadDocument:
		var input agenttools.ReadDocumentInput
		if json.Unmarshal(raw, &input) != nil {
			return false
		}
		ids = e.coverageDocumentIDs([]string{strings.TrimSpace(input.ID)})
		plan = input.SourceScope
	case agenttools.ToolSearchKnowledge:
		var input agenttools.SearchKnowledgeInput
		if json.Unmarshal(raw, &input) != nil {
			return false
		}
		ids, plan = input.DocumentIDs, input.SourceScope
		if len(ids) == 0 && plan == nil {
			ids = agenttools.CurrentDocumentScopeIDs(e.toolRegistry)
		}
	case agenttools.ToolBindDocumentScope:
		if json.Unmarshal(raw, &plan) != nil {
			return false
		}
	}
	if plan != nil {
		ids = slices.Clone(ids)
		for _, source := range plan.Sources {
			ids = append(ids, source.KnowledgeID)
		}
	}
	return len(ids) > 0 && !slices.ContainsFunc(ids, func(id string) bool {
		return !slices.ContainsFunc(scope.sources, func(source coverageSourceIdentity) bool {
			return source.domain == "knowledge" && source.id == id
		})
	})
}
