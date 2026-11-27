package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCoverageDraftRanksVerifiedRolesAcrossOrders(t *testing.T) {
	item := func(metric, id, day string, value int) coverageEvidence {
		return coverageEvidence{
			Kind: "cell", SourceID: "ledger", SourceTitle: "Ledger", SnapshotHash: "snapshot",
			Metric: metric, SeriesID: metric, Date: day, EvidenceID: id, JSONPointer: "/row",
			Raw: json.Number(fmt.Sprint(value)),
		}
	}
	first := item("Pressure", "a", "2025-04-01", 19)
	second := item("Pressure", "b", "2025-04-02", 23)
	third := item("Pressure", "c", "2025-04-03", 26)
	duration := item("Duration", "d", "2025-04-01", 40)
	flow := item("Flow", "e", "2025-04-01", 81)
	review := coverageDraftReview{
		record: coverageRecord{Evidence: []coverageEvidence{first, second, third, duration, flow}},
		facts: []coverageDraftFact{
			{coverageFact: coverageFact{"scoped_value", first}, goal: 0, slot: "monthly-pressure"},
			{coverageFact: coverageFact{"scoped_value", second}, goal: 0, slot: "monthly-pressure"},
			{coverageFact: coverageFact{"scoped_value", third}, goal: 0, slot: "monthly-pressure"},
			{coverageFact: coverageFact{"scoped_value", duration}, goal: 1, slot: "duration"},
			{coverageFact: coverageFact{"scoped_value", flow}, goal: 2, slot: "flow"},
		},
	}
	const manyValues = "Pressure was 19 on 2025-04-01, 23 on 2025-04-02 and 26 on 2025-04-03."
	const mostRoles = "Duration was 40 on 2025-04-01.\n\nFlow was 81 on 2025-04-01."
	const equalRank = "On 2025-04-01, Flow was 81 and Duration was 40."
	tests := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"more roles forward", []string{manyValues, mostRoles}, mostRoles},
		{"more roles reverse", []string{mostRoles, manyValues}, mostRoles},
		{"ties forward", []string{mostRoles, equalRank}, ""},
		{"ties reverse", []string{equalRank, mostRoles}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.turnReview.Query = "What do the records establish?"
			ctx, state := t.Context(), &types.AgentState{}
			for _, candidate := range test.candidates {
				assessment := e.assessCoverageDraft(review, candidate, state)
				require.NotEmpty(t, assessment.anchors)
				if candidate == manyValues {
					require.Len(t, assessment.anchors, 3)
					require.Len(t, assessment.roles, 1, "three witnessed numbers in one requested role carry one vote")
				} else {
					require.Len(t, assessment.roles, 2)
				}
				decision := candidateDecision{Candidate: candidate, CandidateHash: candidateTextDigest(candidate),
					TaskHash:        e.candidateTaskDigest(e.turnReview.Query, nil),
					SourceScopeHash: e.unverifiedSourceScopeDigest(ctx), StandardReview: true}
				e.rememberCoverageDraft(ctx, decision, assessment, review, state)
			}
			want := test.want
			if want == "" {
				want = mostRoles
				if candidateTextDigest(equalRank) < candidateTextDigest(mostRoles) {
					want = equalRank
				}
			}
			winner, current := e.currentCheckedCoverageDraft(ctx, state, review)
			require.True(t, current)
			require.Equal(t, want, winner.Candidate)

			// Projection order cannot change an already verified winner or give
			// the holder permission to use a changed receipt.
			review := review
			review.facts = slices.Clone(review.facts)
			slices.Reverse(review.facts)
			_, current = e.currentCheckedCoverageDraft(ctx, state, review)
			require.True(t, current)
			review.facts = nil
			review.record.Evidence = nil
			_, current = e.currentCheckedCoverageDraft(ctx, state, review)
			require.False(t, current)
		})
	}
}

func TestCoverageDraftNeedsDisplayedValueWithItsOwnFieldAndCitation(t *testing.T) {
	const cite = `<kb doc="Weekly report" chunk_id="table-page" kb_id="library" />`
	evidence := coverageEvidence{
		Kind: "body", ChunkID: "table-page", Metric: "Inventory", Subject: "North",
		ReportedValue: "42", Date: "2025-04-01", Unit: "kg", UnitBasis: "header",
	}
	for _, test := range []struct {
		name, text string
		present    bool
	}{
		{"verified fields, citation and units", "North Inventory was 42 kg on 2025-04-01 " + cite, true},
		{"same value in another field", "North Inbound was 42 kg on 2025-04-01 " + cite, false},
		{"same value in another group", "South Inventory was 42 kg on 2025-04-01 " + cite, false},
		{"same paragraph unrelated value", "North Inventory on 2025-04-01 is unavailable. South Inbound was 42 kg " + cite, false},
		{"cited field with incompatible unit", "North Inventory was 42 tonnes on 2025-04-01 " + cite, false},
		{"unit belongs to another assertion", "North Inventory was 42 tonnes on 2025-04-01. South Inbound was 51 kg " + cite, false},
		{"wrong date despite a nearby date", "North Inventory on 2025-04-01 is unavailable. North Inventory was 42 kg on 2025-04-02 " + cite, false},
		{"citation alone", "North Inventory on 2025-04-01 " + cite, false},
		{"value with citation elsewhere", "North Inventory was 42 kg on 2025-04-01", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.present, newCoverageDraftParagraph(test.text).displays(evidence, "scoped_value", true))
		})
	}
	other := evidence
	other.Metric, other.Column = "Inbound", "F"
	evidence.Column = "E"
	facts := []coverageDraftFact{
		{coverageFact: coverageFact{Role: "scoped_value", Evidence: evidence}},
		{coverageFact: coverageFact{Role: "scoped_value", Evidence: other}},
	}
	ambiguous := newCoverageDraftParagraph("North Inventory and North Inbound on 2025-04-01 appear here with a figure of 42 kg. " + cite)
	explicit := newCoverageDraftParagraph("North Inventory was 42 kg on 2025-04-01. North Inbound was 42 kg on 2025-04-01. " + cite)
	for _, fact := range facts {
		require.False(t, ambiguous.displaysVerifiedRole(fact, facts), "one numeric operand cannot discharge both equal rows of the cited evidence")
		require.True(t, explicit.displaysVerifiedRole(fact, facts))
	}

}
