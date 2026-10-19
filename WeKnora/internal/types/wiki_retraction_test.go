package types

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWikiRetractionStatePreservesOtherMetadataAndTracksMultipleSources(t *testing.T) {
	page := &WikiPage{PageMetadata: JSON(`{"topic":"materials","custom":{"rank":3}}`)}
	SetWikiRetractionSource(page, "source-a", false)
	SetWikiRetractionSource(page, "source-b", false)
	SetWikiRetractionSource(page, "source-c", true)

	state := ReadWikiRetractionState(page)
	require.Equal(t, []string{"source-a", "source-b"}, state.PendingSourceIDs)
	require.Equal(t, []string{"source-c"}, state.FailedSourceIDs)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(page.PageMetadata, &metadata))
	require.Equal(t, "materials", metadata["topic"])
	require.NotNil(t, metadata["custom"])

	ClearWikiRetractionSources(page, []string{"source-a"})
	state = ReadWikiRetractionState(page)
	require.Equal(t, []string{"source-b"}, state.PendingSourceIDs)
	require.Equal(t, []string{"source-c"}, state.FailedSourceIDs)
}

func TestOrdinaryWikiMetadataUsesStoredMarkerAsAuthority(t *testing.T) {
	current := WikiPage{PageMetadata: JSON(`{"_wiki_retraction":{"pending_source_ids":["source-a"]}}`)}
	incoming := JSON(`{"topic":"revised"}`)
	merged := PreserveWikiRetractionState(incoming, current.PageMetadata)
	page := &WikiPage{PageMetadata: merged}
	require.Equal(t, []string{"source-a"}, ReadWikiRetractionState(page).PendingSourceIDs)

	// A stale writer carrying the old marker cannot reactivate it after the
	// reviewed transaction has cleared the stored marker.
	staleIncoming := page.PageMetadata
	current.PageMetadata = JSON(`{"topic":"revised"}`)
	cleared := PreserveWikiRetractionState(staleIncoming, current.PageMetadata)
	require.Empty(t, ReadWikiRetractionState(&WikiPage{PageMetadata: cleared}).PendingSourceIDs)

	ctx := WithWikiRetractionMetadataMutation(context.Background())
	require.True(t, WikiRetractionMetadataMutationAllowed(ctx))
}

func TestFailedWikiRetractionOnlyTransitionsAnExistingPendingSource(t *testing.T) {
	page := &WikiPage{}
	require.False(t, FailPendingWikiRetractionSource(page, "source-a"))
	SetWikiRetractionSource(page, "source-a", false)
	require.True(t, FailPendingWikiRetractionSource(page, "source-a"))
	state := ReadWikiRetractionState(page)
	require.Empty(t, state.PendingSourceIDs)
	require.Equal(t, []string{"source-a"}, state.FailedSourceIDs)
}
