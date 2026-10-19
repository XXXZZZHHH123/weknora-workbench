package agent

import (
	"fmt"
	"strings"
)

const (
	maxReviewReceiptDiagnostics     = 8
	maxReviewReceiptDiagnosticRunes = 2400
	reviewReceiptDiagnosticHeader   = "accepted source review diagnostics: "
	reviewReceiptDiagnosticOverflow = "; additional diagnostics omitted"
)

type reviewProtocolIssueKey struct{}

type reviewClaimCheck struct {
	Claim      string                `json:"claim"`
	Basis      string                `json:"basis"`
	Assessment string                `json:"assessment"`
	Evidence   []reviewSourceWitness `json:"evidence"`
}

// This checks identity/exposure only. The model reviews the whole answer and
// records a small set of material checks; a receipt does not prove entailment.
// The runtime binds the verdict to the whole candidate, task and source snapshot;
// forcing an exact substring here would mistake Markdown layout for meaning.
func validateReviewReceipt(answer, overallAssessment string, checks []reviewClaimCheck, snapshot *sourceReviewSnapshot) error {
	if strings.TrimSpace(answer) == "" || strings.TrimSpace(overallAssessment) == "" || len(checks) == 0 || len(checks) > 16 || snapshot == nil {
		return fmt.Errorf("accepted source review requires an overall assessment and bounded critical audit")
	}
	diagnostics := reviewReceiptDiagnostics{}
	validatedCriticalCheck := false
	for entryIndex, check := range checks {
		label := fmt.Sprintf("entry[%d]", entryIndex)
		checkValid := true
		if strings.TrimSpace(check.Claim) == "" || strings.TrimSpace(check.Assessment) == "" {
			diagnostics.add(label, "audit requires a critical claim description and a substantive assessment")
			checkValid = false
		}
		switch check.Basis {
		case "source", "inference", "limitation":
			if len(check.Evidence) == 0 {
				diagnostics.add(label, "source, inference and limitation audits require inspected evidence")
				checkValid = false
			}
		case "presentation":
		default:
			diagnostics.add(label, "unknown audit basis")
			checkValid = false
		}
		observedSource := false
		validatedWitness := false
		for _, witness := range check.Evidence {
			if err := reviewWitnessVisible(snapshot.input, snapshot.sourceRecords, witness); err != nil {
				diagnostics.add(label, err.Error())
				checkValid = false
			} else {
				validatedWitness = true
				observedSource = observedSource || witness.Kind == "source"
			}
		}
		if (check.Basis == "source" || check.Basis == "inference") && validatedWitness && !observedSource {
			diagnostics.add(label, "execution-status witnesses cannot support source facts")
			checkValid = false
		}
		if checkValid && check.Basis != "presentation" && validatedWitness {
			validatedCriticalCheck = true
		}
	}
	if !validatedCriticalCheck {
		diagnostics.add("audit", "accepted source review requires at least one supported substantive critical check")
	}
	return diagnostics.err()
}

type reviewReceiptDiagnostics struct {
	entries []string
	runes   int
	omitted bool
}

func (d *reviewReceiptDiagnostics) add(label, message string) {
	if len(d.entries) >= maxReviewReceiptDiagnostics {
		d.omitted = true
		return
	}
	entry := label + ": " + message
	separatorRunes := 0
	if len(d.entries) > 0 {
		separatorRunes = 2 // "; "
	}
	limit := maxReviewReceiptDiagnosticRunes - len([]rune(reviewReceiptDiagnosticHeader)) - d.runes - separatorRunes - len([]rune(reviewReceiptDiagnosticOverflow))
	if limit <= 0 {
		d.omitted = true
		return
	}
	entryRunes := []rune(entry)
	if len(entryRunes) > limit {
		if limit > 0 {
			entryRunes = append(entryRunes[:limit-1], '…')
		}
		d.omitted = true
	}
	d.entries = append(d.entries, string(entryRunes))
	d.runes += len(entryRunes) + separatorRunes
}

func (d reviewReceiptDiagnostics) err() error {
	if len(d.entries) == 0 {
		return nil
	}
	message := reviewReceiptDiagnosticHeader + strings.Join(d.entries, "; ")
	if d.omitted {
		message += reviewReceiptDiagnosticOverflow
	}
	return fmt.Errorf("%s", message)
}

const sourceReviewReceiptInstructions = `Judge the whole candidate against the user's request before writing the receipt. Acceptance means the exact candidate is supported, including its scope, interpretation and limitations; matching numbers or a valid source identity alone is not support. For each material claim, check what the cited observation actually establishes about that same object, period and relation. Do not combine unrelated claims under a shared witness. If an unsupported aside is unnecessary to answer the request, return revise_answer to remove it while retaining the supported main analysis; collect_evidence is for evidence needed to answer the request itself.
For complete=true, give a short overall assessment and 1-3 narrow checks of decisive claims. Do not write a second answer, repeat the candidate, or enumerate every row, number or paragraph. Each entry contains claim, basis (source/inference/limitation/presentation), assessment explaining the evidence-to-claim relation, and evidence. Keep each claim within what its witness actually shows; do not combine unrelated details under one broad claim. At least one substantive check needs a valid witness; presentation alone cannot establish acceptance. Do not rationalize a chosen verdict. For complete=false, omit assessment/audit and give only blocking feedback and next_action. Identify the current candidate wording and actual contradictory or missing evidence; feedback is not new source evidence. Omit feedback/next_action on acceptance.
Choose one witness locator:
- Body already displayed in citation_source_evidence.items: {source_pointer_ref: the exact source_pointer_ref shown on that body item, quote: exact short displayed substring, kind: source}. The server requires one matching main body item and resolves its immutable source identity and path. This value is not an item position; do not infer it from array order or use a pointer from additional_source_observations. No other locator fields.
- A visible structured_query_row_companion cell: {row_ref: {entry_index: exact entry_index printed on that entry, row_index: zero-based visible row, column: exact column name}, kind: source}. No quote, evidence_id, json_pointer or source_pointer_ref. Copy entry_index and column from the displayed entry; do not infer them from another entry's fields. The server resolves the visible cell to its immutable source. Use this only for a visible scalar cell; hidden rows require reading evidence and using the raw locator instead.
- Other data or newly read evidence: {evidence_id: actual source ID, quote: exact displayed scalar, kind: source}. When you already have an exact source path, quote may be omitted only in {evidence_id: actual source ID, json_pointer: exact path shown by the reader, kind: source}. For a scalar leaf the server derives the scalar from that same immutable source value. For a map/array, it derives only the complete compact JSON at that exact path (maximum 1024 bytes); the entire container must itself be visible in the current source view or fully inspected at that exact path. A child read, partial projection or partial page does not expose its parent container. Omitting quote never locates a value, accepts a hidden field, or applies to execution evidence. Prefer omitting json_pointer when an explicitly supplied quote uniquely identifies a scalar in successful result data. Never guess an index or point to a container while quoting one of its children. Do not quote JSON key/value fragments or serialized arrays.
A limitation's basis does not dictate its witness kind. Successful returned mapping/catalog states, data-quality statistics and observation coverage are source data: use kind=source and evidence for the actual named source and scope. A generic notice about how to interpret metadata is not that source's state. Only actual tool execution status/scope uses kind=execution with an explicit status/scope pointer. Tool schemas, plans, failed calls and runtime metadata cannot prove source facts. Only successful wiki_search/wiki_read_page additionally allow exact /output text as a source; other tools' /output is not a source locator.
Inspect any relevant omitted evidence with read_review_evidence before deciding. Partial text, pagination, a search miss or a listed source does not prove absence or full inspection. If the original result lacks required evidence, reject with collect_evidence; if existing evidence suffices, use revise_answer for interpretation or unnecessary claims. Preserve unresolved source conflicts. The program validates identities, exact values and actual visibility; you remain responsible for the entire answer's semantic support and task completion.`
