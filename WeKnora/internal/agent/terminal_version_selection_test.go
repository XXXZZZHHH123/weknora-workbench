package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerminalVersionChoiceRequiresCurrentConflictingReceipts(t *testing.T) {
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
