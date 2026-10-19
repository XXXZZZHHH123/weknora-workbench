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

func TestPointerlessQuoteRepeatedWithinOneItemBindsToThatItem(t *testing.T) {
	text := "外销订单依旧维持小单"
	record, reader, raw := receiptFixture(t, "chunk-call", `{"success":true,"data":{"chunks":[{"content":"其他内容"},{"content":"`+text+`。","match_snippet":"`+text+`","source_locators":[{"quote":"`+text+`"}]}]}}`)
	input := receiptVisibleInput(record, raw)
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, Quote: text, Kind: "source"}))

	// The same text in two different returned items stays ambiguous.
	record, reader, raw = receiptFixture(t, "two-chunks", `{"success":true,"data":{"chunks":[{"content":"`+text+`"},{"content":"`+text+`"}]}}`)
	input = receiptVisibleInput(record, raw)
	require.ErrorContains(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, Quote: text, Kind: "source"}), "multiple scalar paths")
	require.False(t, sameReviewItemRoot([]string{"/data/left", "/data/right"}))
	require.False(t, sameReviewItemRoot([]string{"/data/chunks/2/content", "/data/image_catalog/0/source_context"}))
	require.True(t, sameReviewItemRoot([]string{"/data/chunks/1/content", "/data/chunks/1/source_locators/0/quote"}))
}
