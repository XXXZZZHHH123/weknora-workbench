package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func acceptedReceiptSnapshotFixture(t *testing.T) (*sourceReviewSnapshot, reviewSourceWitness) {
	t.Helper()
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "observed-call", Content: `{"success":true,"data":{"content":"The survey covers the eastern region only; the unit is hectares."}}`}
	records := newReviewEvidenceReader([]chat.Message{message})
	var payload any
	require.NoError(t, json.Unmarshal([]byte(message.Content), &payload))
	snapshot := &sourceReviewSnapshot{sourceRecords: records, input: map[string]any{
		"source_tool_results": []reviewToolEvidence{{EvidenceID: records.records[0].ID, Tool: message.Name, ToolCallID: message.ToolCallID, Content: payload, ViewComplete: true}},
	}}
	return snapshot, reviewSourceWitness{EvidenceID: records.records[0].ID, JSONPointer: "/data/content", Quote: "The survey covers the eastern region only; the unit is hectares.", Kind: "source"}
}

func TestReviewReceiptRequiresOverallAssessmentAndSupportedCriticalCheck(t *testing.T) {
	snapshot, witness := acceptedReceiptSnapshotFixture(t)
	answer := "The survey covers the eastern region.\n\nIts unit is hectares."
	critical := reviewClaimCheck{Claim: "eastern region", Basis: "source", Assessment: "The source explicitly limits the survey region.", Evidence: []reviewSourceWitness{witness}}
	require.NoError(t, validateReviewReceipt(answer, "The answer is appropriately scoped to the observed survey.", []reviewClaimCheck{critical}, snapshot))

	// The model must review the whole candidate, but the protocol records only
	// material checks rather than mechanically enumerating every paragraph.
	require.NoError(t, validateReviewReceipt(answer, "The candidate answers the request without an unsupported material claim.", []reviewClaimCheck{critical}, snapshot))
	require.Error(t, validateReviewReceipt(answer, "", []reviewClaimCheck{critical}, snapshot))
	require.Error(t, validateReviewReceipt(answer, "The answer is complete.", nil, snapshot), "empty audit cannot accept")

	presentationOnly := reviewClaimCheck{Claim: "The survey covers", Basis: "presentation", Assessment: "Heading and connective text are harmless.", Evidence: nil}
	require.Error(t, validateReviewReceipt(answer, "Everything looks good.", []reviewClaimCheck{presentationOnly}, snapshot), "presentation cannot establish acceptance")

	for name, mutate := range map[string]func(*reviewClaimCheck){
		"empty claim":        func(c *reviewClaimCheck) { c.Claim = " " },
		"missing assessment": func(c *reviewClaimCheck) { c.Assessment = " " },
	} {
		t.Run(name, func(t *testing.T) {
			changed := critical
			changed.Evidence = append([]reviewSourceWitness(nil), critical.Evidence...)
			mutate(&changed)
			require.Error(t, validateReviewReceipt(answer, "The answer is scoped.", []reviewClaimCheck{changed}, snapshot))
		})
	}
	// Evidence locating is audited, not a delivery gate (user decision).
	for name, tc := range map[string]struct {
		mutate              func(*reviewClaimCheck)
		verified, unlocated int
	}{
		"missing evidence": {func(c *reviewClaimCheck) { c.Evidence = nil }, 0, 0},
		"invented quote":   {func(c *reviewClaimCheck) { c.Evidence[0].Quote = "The survey covers every region." }, 0, 1},
		"unrelated result": {func(c *reviewClaimCheck) { c.Evidence[0].EvidenceID = "not-an-observed-result" }, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			changed := critical
			changed.Evidence = append([]reviewSourceWitness(nil), critical.Evidence...)
			tc.mutate(&changed)
			require.NoError(t, validateReviewReceipt(answer, "The answer is scoped.", []reviewClaimCheck{changed}, snapshot))
			summary := reviewReceiptWitnessSummary([]reviewClaimCheck{changed}, snapshot)
			require.Equal(t, tc.verified, summary["verified"])
			if name == "invented quote" {
				require.Equal(t, tc.unlocated, summary["unlocated"], "invented text is recorded as unlocated")
			}
		})
	}
}

func TestCriticalCheckDescribesCandidateAcrossPresentationBoundaries(t *testing.T) {
	snapshot, witness := acceptedReceiptSnapshotFixture(t)
	checks := []reviewClaimCheck{{Claim: "The survey is limited to the eastern region and uses hectares.",
		Basis: "source", Assessment: "The two displayed facts match the source scope and unit.", Evidence: []reviewSourceWitness{witness}}}
	answer := "Region: **eastern region**.\n\nUnit: **hectares**."
	require.NoError(t, validateReviewReceipt(answer, "The whole candidate preserves the reported region and unit.", checks, snapshot))
	// Describing the claim freely does not relax the independent source check.
	checks[0].Evidence[0].Quote = "The survey covers all regions."
	require.NoError(t, validateReviewReceipt(answer, "Reviewed", checks, snapshot))
	require.Equal(t, 1, reviewReceiptWitnessSummary(checks, snapshot)["unlocated"], "an invented quote is recorded, not silently verified")
}

// Mapping metadata is evidence of a scoped limitation, not an observation value.
// The semantic distinction is reviewed by the model; identity and visibility
// remain mandatory even for an answer that only asks the user to map fields.
func TestMappingLimitationReceiptRequiresActualVisibleState(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "catalog-call", Content: `{"success":true,"data":{"operation":"list_series","dataset_id":"observed-dataset","series":[],"total":0,"offset":0,"next_offset":null,"truncated":false,"match_mode":"pending_scope","pending_sheets":[{"name":"Measurements","status":"needs_review","reasons":["field mapping pending"]}]}}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	var payload any
	require.NoError(t, json.Unmarshal([]byte(message.Content), &payload))
	snapshot := &sourceReviewSnapshot{sourceRecords: reader, input: map[string]any{
		"source_tool_results": []reviewToolEvidence{{EvidenceID: reader.records[0].ID, Tool: message.Name, ToolCallID: message.ToolCallID, Content: payload, ViewComplete: true}},
	}}
	witness := reviewSourceWitness{EvidenceID: reader.records[0].ID, JSONPointer: "/data/pending_sheets/0/reasons/0", Quote: "field mapping pending", Kind: "source"}
	check := reviewClaimCheck{Claim: "The requested sheet still needs field mapping.", Basis: "limitation", Assessment: "The returned catalog explicitly marks the requested scope as pending.", Evidence: []reviewSourceWitness{witness}}
	answer := "Please finish mapping the fields in Measurements before querying them."
	require.NoError(t, validateReviewReceipt(answer, "The answer reports the current mapping state only.", []reviewClaimCheck{check}, snapshot))
	require.Equal(t, 1, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["verified"])
	check.Evidence[0].Quote = "source file could not be read"
	require.NoError(t, validateReviewReceipt(answer, "Reviewed", []reviewClaimCheck{check}, snapshot))
	require.Equal(t, 1, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["unlocated"])
	check.Evidence[0] = witness
	snapshot.input = map[string]any{}
	require.Equal(t, 0, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["verified"], "unread catalog metadata is not counted as verified")
	require.Equal(t, 1, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["located_by_text"])
}
