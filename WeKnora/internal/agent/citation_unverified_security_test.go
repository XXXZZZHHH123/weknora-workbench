package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationWarningRejectsLegacyProjectionOfAnotherRevision(t *testing.T) {
	e, state := citationWarningFixture(t, &mockChat{})
	views := currentCitationSourceViews(state)["view-shared"]
	row := views[0].row
	delete(row, "content_revision")
	raw, err := json.Marshal(row["content_source_view"])
	require.NoError(t, err)
	var proof types.ChunkContentView
	require.NoError(t, json.Unmarshal(raw, &proof))
	proof.OriginRevision++
	row["content_source_view"] = proof
	require.False(t, e.citationWarningIdentitySafe("view-shared", views), "an explicit old projection revision cannot inherit a current revision from another view")
}

func TestCitationWarningRejectsForgedOrDuplicateIdentityAttributes(t *testing.T) {
	_, state := citationWarningFixture(t, &mockChat{})
	views := currentCitationSourceViews(state)["view-shared"]
	for _, tag := range []string{
		`<kb chunk_id="view-shared" knowledge_base_id="foreign-library" />`,
		`<kb chunk_id="invented-id" chunk_id="view-shared" />`,
		`<kb chunk_id="view-shared" kb_id="contract-library" kb_id="foreign-library" />`,
	} {
		t.Run(tag, func(t *testing.T) {
			require.False(t, citationWarningTagScopeSafe(tag, views))
		})
	}
}

func TestCitationValidChunkWithForgedLibraryStillRefuses(t *testing.T) {
	e, state := citationWarningFixture(t, &mockChat{})
	draft := `另段说明。<kb chunk_id="other-current" kb_id="foreign-library" />`
	d := e.checkCandidate(t.Context(), "说明内容。", draft, "", state, nil, false)
	require.Equal(t, "citation_identity_invalid", d.ReasonCode)
	require.NotEqual(t, candidateAccepted, d.Disposition)
	e.commitTerminalResult(t.Context(), state, "citation", answerReviewUnavailableFallback, "", "failed", false)
	require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer)
	require.Nil(t, e.turnReview.CitationUnverifiedDelivery)
}

func TestCitationWarningDoesNotRewriteCodeOrLinkExamples(t *testing.T) {
	for _, format := range []string{"`%s`", "```xml\n%s\n```", "[example](https://example.invalid/%s)"} {
		t.Run(strings.ReplaceAll(format, "\n", "_"), func(t *testing.T) {
			e, state := citationWarningFixture(t, &mockChat{})
			invalidTag := `<kb chunk_id="view-shared" />`
			draft := "正文。\n\n" + strings.Replace(format, "%s", invalidTag, 1) + "\n\n" + `另段。<kb chunk_id="other-current" />`
			e.checkCandidate(t.Context(), "保留原格式说明。", draft, "", state, nil, false)
			e.commitTerminalResult(t.Context(), state, "citation-code", answerReviewUnavailableFallback, "", "failed", false)
			require.Nil(t, e.turnReview.CitationUnverifiedDelivery, "a citation-looking example in protected syntax cannot be rewritten as prose")
			require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer)
		})
	}
}
