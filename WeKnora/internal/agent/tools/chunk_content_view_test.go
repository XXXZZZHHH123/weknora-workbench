package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func contentViewText(t *testing.T, original string, view *types.ChunkContentView) string {
	t.Helper()
	runes := []rune(original)
	var text strings.Builder
	for _, span := range view.Ranges {
		require.GreaterOrEqual(t, span.Start, 0)
		require.LessOrEqual(t, span.End, len(runes))
		text.WriteString(string(runes[span.Start:span.End]))
	}
	return text.String()
}

func TestReadDocumentPageViewKeepsExactRepositoryUnicodeRangesAcrossPagination(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	const original = "<!-- PDF page 6 -->\nEarlier 🐢.\n<!-- PDF page 7 -->\n面积🙂用量。\n<!-- PDF page 7 -->\n第二段 é.\n<!-- PDF page 8 -->\nOther page.\n"
	repo.ordered[0].Content, repo.ordered[0].ContentRevision = original, 4
	repo.ordered[0].SourceLocators = types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 7}, {Type: types.SourceLocatorPDF, Page: 8}}
	digest := sha256.Sum256([]byte(original))
	for _, input := range []string{
		`{"id":"doc-1","page":7}`,
		`{"id":"doc-1","page":7,"content_source_view":{"origin_sha256":"forged-model-hash","origin_revision":99,"ranges":[{"start":0,"end":1}]}}`,
		`{"id":"chunk-0","page":7,"content_offset":2,"content_limit":5}`,
		`{"id":"chunk-0","content_offset":2,"content_limit":5}`,
	} {
		t.Run(input, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), json.RawMessage(input))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			row := result.Data["chunks"].([]map[string]any)[0]
			view := row["content_source_view"].(*types.ChunkContentView)
			require.Equal(t, hex.EncodeToString(digest[:]), view.OriginSHA256)
			require.Equal(t, 4, view.OriginRevision)
			require.Equal(t, len([]rune(original)), view.OriginRuneCount)
			require.Equal(t, row["content"], contentViewText(t, original, view), "page-relative pagination must compose into original chunk rune coordinates")
			require.Equal(t, 4, row["content_revision"])
			require.Equal(t, repo.ordered[0].SourceLocators, row["source_locators"])
			if input == `{"id":"doc-1","page":7}` {
				require.Len(t, view.Ranges, 2, "a repeated real page boundary retains both exact source spans")
				require.NotContains(t, row["content"], "Earlier")
				require.NotContains(t, row["content"], "Other page")
			}
			require.Equal(t, original, repo.ordered[0].Content, "reading a presentation cannot rewrite the stored source")
		})
	}
}

func TestReadDocumentPageViewRangeBudgetDoesNotInventAProof(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	repo.ordered[0].Content = strings.Repeat("<!-- PDF page 7 -->\nA.\n", types.MaxChunkContentViewRanges+1)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":7}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	row := result.Data["chunks"].([]map[string]any)[0]
	require.NotContains(t, row, "content_source_view", "an unrepresentable projection retains the old conservative conflict policy")
	require.Equal(t, types.MaxChunkContentViewRanges+1, strings.Count(row["content"].(string), "A."))
}

func TestReadDocumentUnchangedPhysicalPageRetainsOriginForLaterContentPagination(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	const original = "Area🙂 note.\nSecond line."
	repo.ordered[0].Content = original
	repo.ordered[0].Metadata = types.JSON(`{"page_number":7}`)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":7}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	row := result.Data["chunks"].([]map[string]any)[0]
	require.Equal(t, original, row["content"])
	require.NotContains(t, row, "content_source_view", "unchanged legacy page views keep the existing search/read compatibility")
	partial, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"chunk-0","page":7,"content_offset":2,"content_limit":5}`))
	require.NoError(t, err)
	require.True(t, partial.Success, partial.Error)
	part := partial.Data["chunks"].([]map[string]any)[0]
	view := part["content_source_view"].(*types.ChunkContentView)
	digest := sha256.Sum256([]byte(original))
	require.Equal(t, hex.EncodeToString(digest[:]), view.OriginSHA256)
	require.Equal(t, []types.ChunkRuneRange{{Start: 2, End: 7}}, view.Ranges)
	require.Equal(t, part["content"], contentViewText(t, original, view))
}

func TestIdentityChunkContentViewRequiresExactHashVersionAndContiguousCoverage(t *testing.T) {
	chunk := &types.Chunk{Content: "界🙂abcd", ContentRevision: 3}
	for _, test := range []struct {
		name   string
		change func(*types.ChunkContentView)
	}{
		{"wrong-hash", func(view *types.ChunkContentView) { view.OriginSHA256 = strings.Repeat("0", 64) }},
		{"wrong-version", func(view *types.ChunkContentView) { view.OriginRevision++ }},
		{"wrong-length", func(view *types.ChunkContentView) { view.OriginRuneCount++ }},
		{"gap", func(view *types.ChunkContentView) { view.Ranges[1].Start++ }},
		{"overlap", func(view *types.ChunkContentView) { view.Ranges[1].Start-- }},
		{"partial", func(view *types.ChunkContentView) { view.Ranges[1].End-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := chunkContentView(chunk, []types.ChunkRuneRange{{Start: 0, End: 2}, {Start: 2, End: 6}})
			require.True(t, identityChunkContentView(chunk, view), "adjacent ranges may cover an unchanged full body")
			test.change(view)
			require.False(t, identityChunkContentView(chunk, view))
		})
	}
}
