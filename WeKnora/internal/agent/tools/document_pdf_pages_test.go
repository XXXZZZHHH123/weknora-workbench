package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadDocumentRemotePDFPhysicalPageAcrossChunkBoundaries(t *testing.T) {
	tool, repo := newReadDocumentFixture(4)
	repo.ordered[0].Content = "<!-- PDF page 6 -->\nEarlier physical page.\n<!-- PDF page 7 -->"
	repo.ordered[1].Content = "[来源定位：PDF 物理页 7/9。印刷页码另计。]\nPrinted label 003.\nRequested body."
	repo.ordered[2].Content = "Requested continuation.\n![Figure](requested.png)\n<!-- PDF page 8 -->\nForeign body.\n![Figure](foreign.png)"
	repo.ordered[2].ImageInfo = `[{"url":"requested.png"},{"url":"foreign.png"}]`
	repo.ordered[3].Content = "<!-- PDF page 9 -->\nLast physical page."

	first, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":7,"limit":1}`))
	require.NoError(t, err)
	require.True(t, first.Success, first.Error)
	require.Equal(t, []string{"chunk-1"}, chunkIDsFromData(t, first.Data))
	require.Contains(t, first.Output, "Requested body.")
	require.NotContains(t, first.Output, "Earlier physical page.")
	require.Equal(t, 1, first.Data["next_offset"])
	require.Equal(t, false, first.Data["page_complete"])

	next, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":7,"offset":1}`))
	require.NoError(t, err)
	require.True(t, next.Success, next.Error)
	require.Equal(t, []string{"chunk-2"}, chunkIDsFromData(t, next.Data))
	require.Contains(t, next.Output, "Requested continuation.")
	require.Contains(t, next.Output, "requested.png")
	require.NotContains(t, next.Output, "Foreign body.")
	require.NotContains(t, next.Output, "foreign.png")
	require.NotContains(t, next.Output, "Last physical page.")
	require.Equal(t, 7, next.Data["page"])

	printed, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":3}`))
	require.NoError(t, err)
	require.False(t, printed.Success, "printed labels cannot establish a physical page")
}

func TestReadDocumentPDFPageMentionsAreNotParserBoundaries(t *testing.T) {
	tool, repo := newReadDocumentFixture(2)
	repo.ordered[0].Content = "The contents list PDF page 5, but no parser boundary was saved."
	repo.ordered[1].Content = "[来源定位：PDF 物理页 5/8。]\nA body quote alone does not establish a page."
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":5}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotContains(t, result.Data, "document_scope")
}
