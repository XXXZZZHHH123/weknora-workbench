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

const sourceReviewReceiptInstructions = `Review the complete candidate semantically before deciding. If any blocking error exists, reject with concise feedback and the appropriate next_action; do not return a partial acceptance audit. For complete=true, include a concise overall assessment of the entire answer and a small audit of material critical checks only. Each audit entry has claim (a concise description of the material claim being checked; it may summarize wording split across formatting), basis (source/inference/limitation/presentation), assessment (why the claim is supported or appropriately limited), and evidence (witnesses for that check). Do not enumerate every sentence or paragraph, and do not audit headings or noncritical prose. At least one substantive source/inference/limitation check must have a validated witness; presentation-only entries cannot establish acceptance. For complete=false, omit assessment and audit and provide blocking feedback/next_action. Do not select complete=true and then rationalize it.
For a body displayed in citation_source_evidence.items, prefer the compact witness {citation_item: the zero-based index of that item in citation_source_evidence.items, quote: an exact short substring of its displayed content, kind: source}. The program resolves that item through its existing dictionaries to the immutable source ID and path; do not supply source_pointer_ref or reconstruct internal dictionary indexes. Do not mix citation_item with evidence_id/json_pointer. For other source data or newly read excerpts, use evidence_id plus quote copied exactly from an actually shown scalar value and kind=source. Omit json_pointer when that quote identifies exactly one scalar within that successful result data: the program resolves the path, so do not guess field names or array indexes. Repeated values require the explicit correct json_pointer. Copy a text value without its JSON key or extra quoting; a number or boolean uses its exact JSON spelling. An explicit pointer to a small fully displayed object/array can quote a literal scalar it contains; the program requires that entire exact container to have been exposed. Prefer the exact scalar path for partial or large containers. Do not quote serialized key/value fragments. For execution-status/coverage limitations use basis=limitation, kind=execution and an explicit status/scope pointer; execution is not an audit basis. Quote enough context to verify attribution and the claimed period/unit/scope. For wiki_search and wiki_read_page only, the exact /output text is also an eligible source witness when actually shown or read; use the explicit /output pointer for rendered search counts, page links or read outcomes. A linked neighbor is not an actual search hit. Other tool output and success metadata are not source facts. Source facts, inferences and limitations need witnesses. Tool schemas, plans, failed calls and runtime metadata do not prove source facts. Partial projected text is NOT inspected full text. Use read_review_evidence for the specific omitted path needed for a claim; listing results or reading unrelated metadata does not discharge that check. If the original tool never returned evidence necessary for a claimed value or source-content assertion, reject with collect_evidence. An accurately scoped mapping-pending notice is instead a limitation supported by actual mapping/catalog state; do not demand observation values to validate that notice. Never accept a source-content claim while its relevant evidence is unread. Preserve uncertainty instead of silently resolving conflicting source statements.
For complete=false, feedback must identify an exact phrase present in THIS candidate and the actually inspected contradictory or missing evidence. Do not critique an earlier draft, invent a replacement page, or describe a projected omission as absence in the original. Check the relevant returned body before claiming a phrase is absent. For complete=true, omit feedback/next_action. The program verifies receipt identities, exact quotations and visibility; you must judge the entire answer's semantic support and whether the full user request is met. Do not mark a claim supported just because its numbers occur somewhere in the input.`
