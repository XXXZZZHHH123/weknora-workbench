package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateReviewReceiptReportsIndependentEntryFailuresTogether(t *testing.T) {
	snapshot, witness := acceptedReceiptSnapshotFixture(t)
	answer := "The survey covers the eastern region.\n\nIts unit is hectares."
	badWitness := witness
	badWitness.EvidenceID = "missing-result"
	checks := []reviewClaimCheck{
		{Claim: "eastern region", Basis: "source", Assessment: "supported", Evidence: []reviewSourceWitness{badWitness, badWitness}},
		{Claim: "hectares", Basis: "unknown", Assessment: "supported", Evidence: []reviewSourceWitness{witness}},
	}
	err := validateReviewReceipt(answer, "Overall review", checks, snapshot)
	require.Error(t, err)
	message := err.Error()
	require.Contains(t, message, "entry[1]")
	require.Contains(t, message, "unknown audit basis")
	require.NotContains(t, message, "witness", "unlocated witnesses are audited, not blocking")
	summary := reviewReceiptWitnessSummary(checks, snapshot)
	require.Equal(t, 1, summary["verified"])
	diagnostic := summary["diagnostics"].(string)
	require.Contains(t, diagnostic, "entry[0]")
	require.Equal(t, 2, strings.Count(diagnostic, "witness does not resolve"), "every witness is audited after an earlier one fails")
	message = diagnostic
	require.NotContains(t, message, witness.Quote, "diagnostics must not disclose source text")
}

func TestValidateReviewReceiptDiagnosticsBoundOutput(t *testing.T) {
	snapshot, _ := acceptedReceiptSnapshotFixture(t)
	answer := "Repeated claim X."
	checks := make([]reviewClaimCheck, 12)
	for index := range checks {
		checks[index] = reviewClaimCheck{Claim: "X", Basis: "unknown", Assessment: "checked"}
	}
	err := validateReviewReceipt(answer, "Overall review", checks, snapshot)
	require.Error(t, err)
	message := err.Error()
	require.Contains(t, message, "entry[0]")
	require.Contains(t, message, "entry[7]")
	require.NotContains(t, message, "entry[8]")
	require.Contains(t, message, "additional diagnostics omitted")
	require.Equal(t, maxReviewReceiptDiagnostics, strings.Count(message, "unknown audit basis"))
	require.LessOrEqual(t, len([]rune(message)), maxReviewReceiptDiagnosticRunes)
	require.NotContains(t, message, "review unit")
}
