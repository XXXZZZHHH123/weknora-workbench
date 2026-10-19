package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Witnesses are never rebound to another path; diagnostics name real options.
func TestWitnessDiagnosticsNameActualPathsWithoutRebinding(t *testing.T) {
	body := "Sampling interval is twenty-nine minutes under the coastal protocol."
	record, reader, raw := receiptFixture(t, "wiki-call", `{"success":true,"data":{"pages":[{"slug":"a","content":"`+body+`"}],"copy":"`+body+`"}}`)
	input := receiptVisibleInput(record, raw)
	err := reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, Quote: body, Kind: "source"})
	require.ErrorContains(t, err, "multiple scalar paths")
	require.ErrorContains(t, err, "/data/copy")
	require.ErrorContains(t, err, "/data/pages/0/content")
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/pages/0/body", Quote: body, Kind: "source"}), "a wrong explicit pointer is not repaired")
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/pages/0/content", Quote: body, Kind: "source"}))
}

func TestMissingPointerDiagnosticNamesActualKeys(t *testing.T) {
	record, _, _ := receiptFixture(t, "keys-call", `{"success":true,"data":{"pages":[{"slug":"a","content":"x"}]}}`)
	_, err := reviewWitnessRawValue(record, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/pages/0/body", Quote: "x", Kind: "source"})
	require.ErrorContains(t, err, `"/data/pages/0"`)
	require.ErrorContains(t, err, "[content,slug]")
}
