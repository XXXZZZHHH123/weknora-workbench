package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxReviewReceiptDiagnostics     = 8
	maxReviewReceiptDiagnosticRunes = 2400
	reviewReceiptDiagnosticHeader   = "accepted source review diagnostics: "
	reviewReceiptDiagnosticOverflow = "; additional diagnostics omitted"
)

type reviewProtocolIssueKey struct{}

type reviewClaimCheck struct {
	Claim              string                `json:"claim"`
	CandidateStatement string                `json:"candidate_statement,omitempty"`
	Fidelity           string                `json:"candidate_fidelity,omitempty"`
	Basis              string                `json:"basis"`
	Assessment         string                `json:"assessment"`
	Evidence           []reviewSourceWitness `json:"evidence"`
}

const (
	reviewFidelityAsWritten    = "as_written"
	reviewFidelityNarrowed     = "narrowed"
	reviewFidelityContradicted = "contradicted"
)

const reviewFidelityFeedbackLimit = 900

// reviewFidelityRevision decides from the reviewer's own declaration, not from
// any wording comparison, whether an acceptance actually verified a different
// (weaker, re-scoped or reversed) proposition than the candidate states. Such
// an acceptance cannot deliver the unchanged candidate: the verified claim is
// returned to the generator as revision advice and the revised text is reviewed
// again. Missing declarations are a protocol defect, never an approval.
func reviewFidelityRevision(checks []reviewClaimCheck) (string, bool, error) {
	var notes []string
	for index, check := range checks {
		statement := strings.TrimSpace(check.CandidateStatement)
		if statement == "" || utf8.RuneCountInString(statement) > 600 {
			return "", false, fmt.Errorf("accepted source review audit[%d] requires candidate_statement with the candidate's own conclusion", index)
		}
		switch check.Fidelity {
		case reviewFidelityAsWritten:
			continue
		case reviewFidelityNarrowed, reviewFidelityContradicted:
			notes = append(notes, fmt.Sprintf("候选表述「%s」%s；证据只支持：「%s」。依据：%s", statement,
				map[string]string{reviewFidelityNarrowed: "需按证据限缩或改写", reviewFidelityContradicted: "与证据相反"}[check.Fidelity],
				strings.TrimSpace(check.Claim), strings.TrimSpace(check.Assessment)))
		default:
			return "", false, fmt.Errorf("accepted source review audit[%d] requires candidate_fidelity as_written, narrowed or contradicted", index)
		}
	}
	if len(notes) == 0 {
		return "", false, nil
	}
	feedback := []rune("审核认定以下结论不能按原文交付，请在最终答复中按证据改正对应标题与正文，不新增其它断言：" + strings.Join(notes, "；"))
	if len(feedback) > reviewFidelityFeedbackLimit {
		feedback = append(feedback[:reviewFidelityFeedbackLimit-1], '…')
	}
	return string(feedback), true, nil
}

// This checks identity/exposure only. The model reviews the whole answer and
// records a small set of material checks; a receipt does not prove entailment.
// The runtime binds the verdict to the whole candidate, task and source snapshot;
// claim wording and semantic goal alignment remain model judgments; Markdown
// transcription is not an additional acceptance gate.
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

const sourceReviewReceiptInstructions = `Judge the whole candidate against the user's request before writing the receipt. Acceptance is judged at the granularity requested by the user: a summary need not enumerate every observation or period, but must preserve material reversals, scope and limitations. Acceptance means the exact candidate is supported, including its scope, interpretation and limitations; matching numbers or a valid source identity alone is not support. For each material claim, check what the cited observation actually establishes about that same object, period and relation. Do not combine unrelated claims under a shared witness. If an unsupported aside is unnecessary to answer the request, return revise_answer to remove it while retaining the supported main analysis; collect_evidence is for evidence needed to answer the request itself.
For complete=true, give a short overall assessment and 1-3 checks of the conclusions that answer the user's task, rather than sampling easy source facts. Do not write a second answer, repeat the candidate, or enumerate every row, number or paragraph. Each entry contains claim, basis (source/inference/limitation/presentation), assessment explaining the evidence-to-claim relation, and evidence. Use advisory_task_plan to focus on the user's requested conclusions, without treating it as extra requirements. Inspect the candidate conclusion as written: do not turn a broad comparison into a narrower true source statement in the receipt. Each entry also states candidate_statement, the candidate's own conclusion as written (including any heading that frames it, its scope, period, direction and causal role), and candidate_fidelity: as_written only when claim and assessment hold for that statement unchanged; narrowed when evidence supports only a weaker, qualified, re-scoped or differently framed version (for example the body concedes what the heading asserts, or factors with opposite effects or different stages are presented as a shared driver); contradicted when evidence opposes it. narrowed/contradicted means the candidate is not accepted unchanged: the runtime returns your supported claim to the generator as revision advice, so the delivered text matches what you verified. Do not mark as_written to avoid a revision. Explain whether the cited evidence supports the actual relation/inference, including contrary evidence. At least one substantive check needs a valid witness; presentation alone cannot establish acceptance. Do not rationalize a chosen verdict. For complete=false, omit assessment/audit and give only blocking feedback and next_action. Identify the current candidate wording and actual contradictory or missing evidence; feedback is not new source evidence. Omit feedback/next_action on acceptance.
Choose one witness locator:
- Body already displayed in citation_source_evidence.items: {source_pointer_ref: the exact source_pointer_ref shown on that body item, quote: exact short displayed substring, kind: source}. The server requires one matching main body item and resolves its immutable source identity and path. This value is not an item position; do not infer it from array order or use a pointer from additional_source_observations. No other locator fields.
- A visible structured_query_row_companion cell: {row_ref: {entry_index: exact entry_index printed on that entry, row_index: zero-based visible row, column: exact column name}, kind: source}. No quote, evidence_id, json_pointer or source_pointer_ref. Copy entry_index and column from the displayed entry; do not infer them from another entry's fields. The server resolves the visible cell to its immutable source. Use this only for a visible scalar cell; hidden rows require reading evidence and using the raw locator instead.
- Other data or newly read evidence: {evidence_id: actual source ID, quote: exact displayed scalar, kind: source}. When you already have an exact source path, quote may be omitted only in {evidence_id: actual source ID, json_pointer: exact path shown by the reader, kind: source}. For a scalar leaf the server derives the scalar from that same immutable source value. For a map/array, it derives only the complete compact JSON at that exact path (maximum 1024 bytes); the entire container must itself be visible in the current source view or fully inspected at that exact path. A child read, partial projection or partial page does not expose its parent container. Omitting quote never locates a value, accepts a hidden field, or applies to execution evidence. Prefer omitting json_pointer when an explicitly supplied quote uniquely identifies a scalar in successful result data. Never guess an index or point to a container while quoting one of its children. Do not quote JSON key/value fragments or serialized arrays.
A limitation's basis does not dictate its witness kind. Successful returned mapping/catalog states, data-quality statistics and observation coverage are source data: use kind=source and evidence for the actual named source and scope. A generic notice about how to interpret metadata is not that source's state. For actual tool failure status, copy the execution_witness supplied beside that result; it points to the visible canonical /error or /success with kind=execution. Other execution scope uses its explicit displayed scope pointer. Tool schemas, plans, failed calls and runtime metadata cannot prove source facts. Only successful wiki_search/wiki_read_page additionally allow exact /output text as a source; other tools' /output is not a source locator.
Inspect any relevant omitted evidence with read_review_evidence before deciding. Partial text, pagination, a search miss or a listed source does not prove absence or full inspection. If the original result lacks required evidence, reject with collect_evidence; if existing evidence suffices, use revise_answer for interpretation or unnecessary claims. Preserve unresolved source conflicts. The program validates identities, exact values and actual visibility; you remain responsible for the entire answer's semantic support and task completion.`
