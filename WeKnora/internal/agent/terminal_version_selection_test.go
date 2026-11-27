package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTerminalVersionChoiceRequiresCurrentConflictingReceipts(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
	for _, mode := range []string{"verified", "unbacked quotation", "different date", "different measure", "different goal", "stale version", "equal disclosure", "conflicting declarations"} {
		t.Run(mode, func(t *testing.T) {
			selected := coverageEvidence{
				Kind: "body", Bound: true, Revision: "request", AuthScope: "authorized-scope",
				SourceID: "report-original", SourceTitle: "Weekly Report", SnapshotHash: "snapshot-a",
				EvidenceID: "receipt-a", JSONPointer: "/passage", SourceDate: "2024-01-12",
				Metric: "Stock", Unit: "tonnes", UnitBasis: "reported", ReportedValue: "6.5",
			}
			alternative := selected
			alternative.SourceID, alternative.SourceTitle = "report-second", "Weekly Report experimental copy"
			alternative.EvidenceID, alternative.SnapshotHash, alternative.ReportedValue = "receipt-b", "snapshot-b", "6.8"
			bind := func(item coverageEvidence) coverageBinding {
				return coverageBinding{Role: "supplement", Kind: "body", SourceID: item.SourceID,
					EvidenceID: item.EvidenceID, JSONPointer: item.JSONPointer, SnapshotHash: item.SnapshotHash}
			}
			choice, other := bind(selected), bind(alternative)
			choice.VersionBasisRef, choice.VersionBasis = coverageObservationRef(alternative), "experimental copy"
			record := coverageRecord{Revision: selected.Revision, AuthScope: selected.AuthScope}
			switch mode {
			case "unbacked quotation":
				choice.VersionBasis = "formally withdrawn"
			case "different date":
				alternative.SourceDate = "2024-02-12"
			case "different measure":
				alternative.Metric = "Output"
			case "different goal":
				other.GoalIndex = 1
			case "stale version":
				record.Snapshots = map[string]string{selected.SourceID: "a-different-snapshot"}
			case "equal disclosure":
				alternative.ReportedValue = selected.ReportedValue
			case "conflicting declarations":
				other.VersionBasisRef, other.VersionBasis = coverageObservationRef(selected), "Weekly Report"
			}
			record.Bindings = []coverageBinding{choice, other}
			record.Evidence = []coverageEvidence{selected, alternative}
			facts := []coverageFact{
				{Role: "supplement_context", Evidence: selected},
				{Role: "supplement_context", Evidence: alternative},
			}
			verified := currentTerminalVersionChoice(record, facts)
			if mode == "verified" {
				require.Equal(t, "Weekly Report", verified.SelectedSource)
				require.Equal(t, "Weekly Report experimental copy", verified.AlternativeSource)
				require.Equal(t, "experimental copy", verified.Basis)
				require.NotEmpty(t, verified.selectedRef)
				require.NotEmpty(t, verified.alternativeRef)
			} else {
				require.Empty(t, verified)
			}
		})
	}
}

func TestA3PrimaryVersionDeclarationUsesCurrentNativeReads(t *testing.T) {
	for _, mode := range []string{"body basis", "pinned version", "title only", "obsolete basis", "one read"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			const library = "scope-library"
			const question = "Use only the selected reports to verify the conflicting Stock disclosures."
			const reason = "The signed correction replaces the preliminary tally."
			const date = "2025-04-09"
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.config.StructuredResultReviewEnabled = false
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 7}}
			records := &coverageScopeDocuments{sourceTimeDocumentStore: &sourceTimeDocumentStore{byID: make(map[string]*types.Knowledge)}}
			bodies := &coverageScopeChunks{bodies: make(map[string]string)}
			observation := func(value string) string { return date + " Stock: " + value + " tonnes." }
			for _, entry := range []struct{ id, value string }{{"draft", "10"}, {"signed", "12"}} {
				doc := &types.Knowledge{ID: entry.id, KnowledgeBaseID: library, TenantID: 7, Title: "Stock update", FileName: entry.id + ".pdf",
					FileType: "pdf", FileHash: strings.Repeat("a", 64), ParseStatus: types.ParseStatusCompleted}
				records.documents = append(records.documents, doc)
				records.byID[doc.ID] = doc
				bodies.bodies[doc.ID] = observation(entry.value)
				if doc.ID == "signed" && (mode == "body basis" || mode == "obsolete basis") {
					bodies.bodies[doc.ID] += "\n" + reason
				}
				e.selectedDocs = append(e.selectedDocs, &SelectedDocumentInfo{KnowledgeID: doc.ID, KnowledgeBaseID: library})
			}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(records, &coverageScopeChunkService{repository: bodies}, e.config.SearchTargets))
			agenttools.PrepareDocumentScope(e.toolRegistry, question)
			refs := []string{e.modelContext.RegisterDocument("draft"), e.modelContext.RegisterDocument("signed")}
			goal := taskCoverageTarget{RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "only", RestrictionBasis: question, Metric: "Stock", VersionTask: "adopt_if_conflict",
				ComparisonSources: refs, Period: map[string]any{"from": date, "to": date, "grain": "day"}, Required: []string{"scoped_value"},
			}}
			if mode == "pinned version" {
				goal.PrimaryRef = refs[1]
			}
			e.taskPlan = &taskEvidencePlan{Goals: []string{"Verify the disclosures"}, CoverageTargets: []taskCoverageTarget{goal}}
			require.NoError(t, validateTaskCoverageBasis(e.taskPlan, question, nil, []string{question}, e.coveragePinnedSelection))
			e.turnReview.Query = question
			state := &types.AgentState{}
			run := func(id, tool string, args any) {
				t.Helper()
				data, err := json.Marshal(args)
				require.NoError(t, err)
				call := e.runToolCall(ctx, types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: tool, Arguments: string(data)}}, 0, 0, 0, "session", "turn")
				require.True(t, call.Result.Success, call.Result.Error)
				step := types.AgentStep{ToolCalls: []types.ToolCall{call}}
				state.RoundSteps = append(state.RoundSteps, step)
				e.modelContext.EncodeMessages(e.appendToolResults(nil, step))
			}
			run("bind", agenttools.ToolBindDocumentScope, agenttools.DocumentScopePlan{Reason: "Both independently selected document identities.",
				Sources: []agenttools.DocumentSourceSelection{{KnowledgeID: "signed", FileName: "signed.pdf"}, {KnowledgeID: "draft", FileName: "draft.pdf"}}})
			run("signed-read", agenttools.ToolReadDocument, map[string]any{"id": "signed"})
			if mode != "one read" {
				run("draft-read", agenttools.ToolReadDocument, map[string]any{"id": "draft"})
			}
			e.refreshEvidenceCompletion(ctx, question, state, nil)
			reading := map[string]string{}
			for ref, item := range e.evidenceCompletion.refs {
				if item.Kind == "body" {
					reading[item.SourceID] = ref
				}
			}
			require.NotEmpty(t, reading["signed"])
			setVersion := func(id, value, cite, quote string) {
				t.Helper()
				params := map[string]any{"goal_index": 0, "role": "primary", "selection_ref": reading[id], "span": observation(value),
					"value_text": value, "metric": "Stock", "unit": "tonnes", "date": date, "period_from": date, "period_to": date, "grain": "day"}
				if cite != "" {
					params["version_alternative_ref"] = reading["draft"]
					params["version_basis_ref"], params["version_basis"] = cite, quote
				}
				raw, err := json.Marshal(params)
				require.NoError(t, err)
				_, accepted := e.acceptCoverageDeclaration(ctx, question, nil, raw)
				require.True(t, accepted, "the actual value remains usable when the optional version choice is unsupported")
			}
			if mode != "one read" {
				setVersion("draft", "10", "", "")
			}
			quote := reason
			if mode == "title only" {
				quote = "Stock update"
			}
			basisRef := reading["signed"]
			if mode == "obsolete basis" {
				basisRef = "obsolete-receipt"
			}
			setVersion("signed", "12", basisRef, quote)
			review := e.currentCoverageDraftReview(ctx, state)
			require.NotEmpty(t, review.facts)
			switch mode {
			case "body basis", "pinned version":
				require.NotEmpty(t, review.version.SelectedReceipt)
				require.NotEqual(t, review.version.SelectedReceipt, review.version.AlternativeReceipt)
				require.NotEmpty(t, review.version.BasisReceipt)
				if mode == "body basis" {
					require.Equal(t, reason, review.version.Basis)
					require.Equal(t, "signed-chunk", review.version.basisChunk)
				} else {
					require.True(t, review.version.UserSelected)
				}
			default:
				require.Empty(t, review.version)
			}

			cite := func(id string) string { return " <kb chunk_id=\"" + id + "-chunk\" />" }
			prose := "The adopted disclosure is " + observation("12") + cite("signed")
			if mode != "one read" {
				prose += "\n\nThe alternative disclosure is " + observation("10") + cite("draft")
			}
			if mode == "body basis" {
				prose += "\n\n" + reason + cite("signed")
				require.True(t, versionChoiceCited(prose, review.version, review.facts))
				swapped := strings.NewReplacer("signed-chunk", "draft-chunk", "draft-chunk", "signed-chunk").Replace(prose)
				require.False(t, versionChoiceCited(swapped, review.version, review.facts), "shared titles do not permit swapped receipts")
				misattributedReason := strings.TrimSuffix(prose, cite("signed")) + cite("draft")
				require.False(t, versionChoiceCited(misattributedReason, review.version, review.facts))
			}
			declaration := map[string]any{"goal_index": 0, "role": "primary", "selection_ref": reading["signed"],
				"version_alternative_ref": firstNonEmptyAgent(reading["draft"], "unread"), "version_basis_ref": basisRef,
				"version_basis": quote, "span": observation("12"), "value_text": "12", "metric": "Stock", "unit": "tonnes", "date": date,
				"period_from": date, "period_to": date, "grain": "day"}
			companion, err := json.Marshal([]map[string]any{declaration})
			require.NoError(t, err)
			candidate := e.acceptCoverageAnswer(ctx, question, state, nil, prose+coverageAnswerTag+string(companion)+coverageAnswerClose, true)
			requiresRevision := mode == "title only" || mode == "one read" || mode == "obsolete basis"
			wantFailure := terminalNoFailure
			if requiresRevision {
				wantFailure = terminalReview
			}
			require.Equal(t, wantFailure, e.terminalCandidateEvidenceFailure(ctx, state, candidate))
			checked := e.checkCandidate(ctx, question, candidate, "", state, nil, false)
			if requiresRevision {
				require.Equal(t, candidateRevise, checked.Disposition)
				require.Equal(t, incompleteVersionEvidence, checked.ReasonCode)
				currentOnly := observation("12") + cite("signed")
				require.Equal(t, terminalNoFailure, e.terminalCandidateEvidenceFailure(ctx, state, currentOnly), "an observed fact is independently available")
			} else {
				require.Equal(t, candidateAccepted, checked.Disposition)
			}
		})
	}
}

func TestA3SupplementCannotRecountThePrimaryWorkbookDocument(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	const day = "2025-04-09"
	const body = day + " Output: 12 tonnes."
	target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "named_first", Required: []string{"scoped_value"}}}
	primary := coverageEvidence{Kind: "gap", SourceDomain: "structured", SourceID: "dataset", DatasetID: "dataset", KnowledgeID: "workbook",
		KnowledgeBaseID: "library", SnapshotHash: "structured-snapshot"}
	document := coverageEvidence{Kind: "body", SourceDomain: "knowledge", SourceID: "workbook", KnowledgeBaseID: "library", SnapshotHash: "document-snapshot",
		Operation: agenttools.ToolReadDocument, Bound: true, Complete: true, ChunkID: "chunk", EvidenceID: "body", JSONPointer: "/document/contents",
		Raw: body, ContentHash: candidateTextDigest(body)}
	binding := coverageBinding{SourceID: document.SourceID, KnowledgeBaseID: "library", Role: "supplement", Kind: "body", EvidenceID: document.EvidenceID,
		Grain: "day", PeriodFrom: day, PeriodTo: day, Date: day, RawClaim: "12", Span: body, Unit: "tonnes", Metric: "Output"}
	baseline := coverageCompletion{State: coverageOpen, PinnedFacts: []coverageFact{{Role: "coverage_boundary", Evidence: primary}}}
	for _, distinct := range []bool{false, true} {
		input := coverageRecord{Evidence: []coverageEvidence{document}, Bindings: []coverageBinding{binding}}
		if distinct {
			input.Evidence[0].SourceID, input.Bindings[0].SourceID = "report", "report"
		}
		result := resolveCoverageSupplement(target, input, baseline, day, day, "day", "", "")
		if !distinct {
			require.Len(t, result.PinnedFacts, 1, "the document path retained the same workbook's physical identity")
			require.Equal(t, coverageOpen, result.State)
			require.Equal(t, body, input.Evidence[0].Raw, "the native read itself remains available")
		} else {
			require.Len(t, result.PinnedFacts, 2)
			require.Equal(t, "supplement", result.PinnedFacts[1].Role)
			require.Equal(t, coverageSatisfied, result.State)
		}
	}
}

func TestA3VersionChoiceRequiresSelectedReadBodiesAndExactReason(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	for _, mode := range []string{"selected original", "selected alternate", "same title", "title only reason", "wrong basis source", "only one body read", "search excerpt", "stale body", "different goal"} {
		t.Run(mode, func(t *testing.T) {
			const reason = "The initial tally overlooked an additional delivery."
			makeReport := func(id, title, amount string) coverageEvidence {
				body := "Stock amounted to " + amount + " tonnes."
				return coverageEvidence{Kind: "body", Operation: agenttools.ToolReadDocument, Bound: true, Complete: true,
					Revision: "request", AuthScope: "grants", SourceID: id, SourceTitle: title, SnapshotHash: "snapshot-" + id,
					KnowledgeBaseID: "authorized-library", EvidenceID: "receipt-" + id, JSONPointer: "/body",
					Date: "2025-04-09", SourceDate: "2025-04-09", Grain: "day", Metric: "Stock", Unit: "tonnes", UnitBasis: "body",
					ReportedValue: amount, Raw: body, ContentHash: candidateTextDigest(body)}
			}
			selected := makeReport("a", "Z report", "10")
			alternative := makeReport("b", "A report", "12")
			if mode == "selected alternate" {
				selected, alternative = alternative, selected
			}
			if mode == "same title" {
				alternative.SourceTitle = selected.SourceTitle
			}
			reasonBody := selected
			reasonBody.EvidenceID, reasonBody.JSONPointer = "reason", "/second-body"
			reasonBody.Raw, reasonBody.ContentHash = reason, candidateTextDigest(reason)
			switch mode {
			case "title only reason":
				reasonBody.Raw, reasonBody.ContentHash, reasonBody.SourceTitle = "Report", candidateTextDigest("Report"), reason
			case "wrong basis source":
				reasonBody.SourceID = "third"
			case "search excerpt":
				reasonBody.Operation = agenttools.ToolSearchKnowledge
			}
			bind := func(item coverageEvidence) coverageBinding {
				return coverageBinding{Role: "supplement", Kind: "body", SourceID: item.SourceID, KnowledgeBaseID: item.KnowledgeBaseID,
					EvidenceID: item.EvidenceID, JSONPointer: item.JSONPointer, SnapshotHash: item.SnapshotHash}
			}
			choice, other := bind(selected), bind(alternative)
			choice.VersionBasisRef, choice.VersionAlternativeRef, choice.VersionBasis = coverageObservationRef(reasonBody), coverageObservationRef(alternative), reason
			if mode == "different goal" {
				other.GoalIndex++
			}
			record := coverageRecord{Revision: "request", AuthScope: "grants", Bindings: []coverageBinding{choice, other},
				Evidence: []coverageEvidence{selected, alternative, reasonBody}}
			facts := []coverageFact{{Role: "supplement", Evidence: selected}, {Role: "supplement", Evidence: alternative}}
			if mode == "only one body read" {
				record.Evidence = []coverageEvidence{selected, reasonBody}
				facts = facts[:1]
			}
			if mode == "stale body" {
				record.Snapshots = map[string]string{alternative.SourceID: "other-snapshot"}
			}
			for range 2 {
				result := currentTerminalVersionChoice(record, facts)
				switch mode {
				case "selected original", "selected alternate", "same title":
					require.Equal(t, coverageFactRef(selected), result.SelectedReceipt)
					require.Equal(t, coverageFactRef(alternative), result.AlternativeReceipt)
					require.Equal(t, coverageFactRef(reasonBody), result.BasisReceipt)
					require.Equal(t, reason, result.Basis)
				default:
					require.Empty(t, result)
				}
				slices.Reverse(facts)
				slices.Reverse(record.Bindings)
				slices.Reverse(record.Evidence)
			}
		})
	}
}
