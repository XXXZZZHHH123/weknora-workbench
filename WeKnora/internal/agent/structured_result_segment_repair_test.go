package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func segmentRepairResponse(candidate, id, replacement string) string {
	data, _ := json.Marshal(map[string]any{"action": "correct", "edits": []map[string]string{{"segment_id": "s_" + candidateTextDigest(candidate)[:12] + "_" + strings.TrimPrefix(id, "line_"), "new": replacement}}})
	return string(data)
}

func TestStructuredResultSegmentRepairAddressesRepeatedTextByOriginalOffsets(t *testing.T) {
	const candidate = "相同原文。\n相同原文。\n末尾不变。"
	directory := newStructuredRepairDirectory(candidate)
	var joined strings.Builder
	for _, segment := range directory.Segments {
		joined.WriteString(segment.Text)
	}
	require.Equal(t, candidate, joined.String())
	reply := segmentRepairResponse(candidate, "line_2", "第二段修正。\n")
	_, _, valid := decodeStructuredResultReview(reply)
	require.False(t, valid, "first editor response cannot use segment addressing")
	repair, valid := decodeStructuredResultRepair(reply)
	require.True(t, valid)
	answer, failure := applyStructuredResultRepair(candidate, directory, repair)
	require.Empty(t, failure)
	require.Equal(t, "相同原文。\n第二段修正。\n末尾不变。", answer)
}

func TestStructuredResultSegmentRepairRejectsWholeAnswerAcrossSeveralLines(t *testing.T) {
	const candidate = "first line\n\nsecond line"
	directory := newStructuredRepairDirectory(candidate)
	repair := &structuredResultRepair{Action: "correct", Edits: []structuredResultRepairEdit{
		{SegmentID: directory.Segments[0].ID, New: "changed first\n"},
		{SegmentID: directory.Segments[2].ID, New: "changed second"},
	}}
	_, failure := applyStructuredResultRepair(candidate, directory, repair)
	require.Equal(t, "segment_whole_answer", failure)
}

func TestStructuredResultSegmentRepairRejectsUnboundOrOverlappingEdits(t *testing.T) {
	const candidate = "first line\nsecond line\n"
	for _, tc := range []struct {
		name    string
		repair  structuredResultRepair
		failure string
	}{
		{"unknown", structuredResultRepair{CandidateHash: candidateTextDigest(candidate), Edits: []structuredResultRepairEdit{{SegmentID: "missing", New: "x"}}}, "segment_identity"},
		{"wrong_hash", structuredResultRepair{CandidateHash: "wrong", Edits: []structuredResultRepairEdit{{SegmentID: newStructuredRepairDirectory(candidate).Segments[0].ID, New: "x"}}}, "segment_candidate_hash"},

		{"duplicate_id", structuredResultRepair{CandidateHash: candidateTextDigest(candidate), Edits: []structuredResultRepairEdit{{SegmentID: newStructuredRepairDirectory(candidate).Segments[0].ID, New: "x"}, {SegmentID: newStructuredRepairDirectory(candidate).Segments[0].ID, New: "y"}}}, "segment_identity"},
		{"mixed_overlap", structuredResultRepair{CandidateHash: candidateTextDigest(candidate), Edits: []structuredResultRepairEdit{{SegmentID: newStructuredRepairDirectory(candidate).Segments[0].ID, New: "x"}, {Old: "first", New: "y"}}}, "patch_overlap"},
		{"unchanged", structuredResultRepair{CandidateHash: candidateTextDigest(candidate), Edits: []structuredResultRepairEdit{{SegmentID: newStructuredRepairDirectory(candidate).Segments[0].ID, New: "first line\n"}}}, "segment_unchanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, failure := applyStructuredResultRepair(candidate, newStructuredRepairDirectory(candidate), &tc.repair)
			require.Equal(t, tc.failure, failure)
		})
	}
	single := "the whole answer"
	repair, valid := decodeStructuredResultRepair(segmentRepairResponse(single, "line_1", "rewritten"))
	require.True(t, valid)
	_, failure := applyStructuredResultRepair(single, newStructuredRepairDirectory(single), repair)
	require.Equal(t, "segment_whole_answer", failure)
	_, failure = applyStructuredResultRepair(candidate+"changed", newStructuredRepairDirectory(candidate), repair)
	require.Equal(t, "segment_candidate_hash", failure)
	oldRepair, valid := decodeStructuredResultRepair(segmentRepairResponse(candidate, "line_1", "repaired\n"))
	require.True(t, valid)
	_, failure = applyStructuredResultRepair(candidate+"changed", newStructuredRepairDirectory(candidate+"changed"), oldRepair)
	require.Equal(t, "segment_identity", failure, "old ID cannot address a new candidate even without echoed hash")
}

func TestStructuredResultSegmentRepairStrictSchemaAndLegacyCompatibility(t *testing.T) {
	for _, reply := range []string{

		`{"action":"correct","candidate_hash":"h","edits":[{"old":"x","segment_id":"line_1","new":"y"}]}`,
		`{"action":"correct","candidate_hash":"h","edits":[{"segment_id":"line_1","segment_id":"line_2","new":"y"}]}`,
		`{"action":"correct","candidate_hash":null,"edits":[{"segment_id":"line_1","new":"y"}]}`,
		`{"action":"correct","candidate_hash":"h","edits":[{"segment_id":"line_1","new":null}]}`,
	} {
		_, valid := decodeStructuredResultRepair(reply)
		require.False(t, valid, reply)
	}
	repair, valid := decodeStructuredResultRepair(`{"action":"correct","edits":[{"old":"first","new":"updated"}]}`)
	require.True(t, valid)
	answer, failure := applyStructuredResultRepair("first\nlast", newStructuredRepairDirectory("first\nlast"), repair)
	require.Empty(t, failure)
	require.Equal(t, "updated\nlast", answer)
	require.True(t, isStructuredResultReviewFormat(structuredResultRepairSchema))
}

func TestStructuredResultSegmentProtocolRecoveryRetainsAllFinalGuards(t *testing.T) {
	const candidate = "Observed 12.500 tonnes.\nObserved 12.500 tonnes.\n"
	for _, invented := range []bool{false, true} {
		t.Run(map[bool]string{false: "supported", true: "invented"}[invented], func(t *testing.T) {
			next := "Observed 12.500 kg.\n"
			if invented {
				next = "Observed 987654321 kg.\n"
			}
			e, state, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{"12.500", "12.500 kg"}), chatAnswer(segmentRepairResponse(candidate, "line_2", next)))
			d := e.checkCandidate(t.Context(), "Report the observations.", candidate, "", state, nil, false)
			require.Len(t, model.calls, 2)
			require.Equal(t, model.calls[0][1].Content, model.calls[1][1].Content)
			require.NotContains(t, model.calls[0][0].Content, "segment_id")
			require.Contains(t, model.calls[1][3].Content, newStructuredRepairDirectory(candidate).Segments[1].ID)
			require.Contains(t, model.calls[1][3].Content, candidateTextDigest(candidate))
			require.Zero(t, e.turnReview.Repairs)
			if invented {
				require.Equal(t, candidate, d.Candidate)
				require.Equal(t, "not_checked_invalid_correction", d.StructuredResultReviewStatus)
			} else {
				require.Equal(t, "corrected", d.StructuredResultReviewStatus)
				require.Equal(t, "Observed 12.500 tonnes.\nObserved 12.500 kg.\n", d.Candidate)
			}
		})
	}
}

func TestStructuredResultSegmentDirectoryCountsAgainstSameInputLimit(t *testing.T) {
	candidate := strings.Repeat("unchanged text.\n", 430)
	e, state, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{"unchanged", "corrected"}))
	d := e.checkStructuredResultDelivery(t.Context(), strings.Repeat("task ", 2000), candidateDecision{Disposition: candidateAccepted, Candidate: candidate}, state, nil)
	require.Len(t, model.calls, 1, "full directory cannot displace original input or cause a second call over budget")
	require.Equal(t, candidate, d.Candidate)
	require.Equal(t, "not_checked_invalid_correction", d.StructuredResultReviewStatus)
	require.Less(t, len(model.calls[0][1].Content), structuredResultReviewInputBytes)
	require.Greater(t, len(model.calls[0][1].Content)+len(structuredResultRepairFeedback(candidate, "patch_not_unique")), structuredResultReviewInputBytes)
}
