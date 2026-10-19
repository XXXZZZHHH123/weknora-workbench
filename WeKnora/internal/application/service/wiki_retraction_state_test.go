package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWikiSlugLockSerializesCleanupAndReducerCallbacks(t *testing.T) {
	var active atomic.Int32
	var overlap atomic.Bool
	start := make(chan struct{})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			acquired, err := withWikiSlugLock(context.Background(), nil, "kb-lock-test", "concept/shared", func(context.Context) error {
				if active.Add(1) != 1 {
					overlap.Store(true)
				}
				time.Sleep(15 * time.Millisecond)
				active.Add(-1)
				return nil
			})
			if err == nil && !acquired {
				err = fmt.Errorf("lock was not acquired")
			}
			done <- err
		}()
	}
	close(start)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.False(t, overlap.Load(), "same KB/slug callbacks must not overlap")
}

func TestWikiSlugLockCanBeReusedByNestedPageServiceWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	innerCalled := false
	acquired, err := withWikiSlugLock(ctx, nil, "kb-nested-lock", "concept/shared", func(lockCtx context.Context) error {
		innerAcquired, innerErr := withWikiSlugLock(lockCtx, nil, "kb-nested-lock", "concept/shared", func(context.Context) error {
			innerCalled = true
			return nil
		})
		require.NoError(t, innerErr)
		require.True(t, innerAcquired)
		return nil
	})
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, innerCalled)
}

func TestWikiPageSnapshotUsesDatabaseMicrosecondPrecision(t *testing.T) {
	current := &types.WikiPage{Slug: "concept/time", UpdatedAt: time.Date(2026, 10, 17, 12, 30, 0, 123456100, time.UTC)}
	zone := time.FixedZone("offset", 3600)
	incoming := &types.WikiPage{Slug: current.Slug, UpdatedAt: current.UpdatedAt.Add(800 * time.Nanosecond).In(zone)}
	require.NoError(t, checkWikiPageSnapshot(incoming, current), "sub-microsecond differences collapse to the same stored timestamp")
	incoming.UpdatedAt = current.UpdatedAt.Add(time.Microsecond)
	require.ErrorIs(t, checkWikiPageSnapshot(incoming, current), ErrWikiPageStaleSnapshot)
}

func TestWikiPageWritesPreservePendingMarkerAndReviewClearsOnlyCoveredSources(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.WikiFolder{}, &types.WikiPage{}, &types.WikiPageRevision{}))
	repo := repository.NewWikiPageRepository(db)
	svc := NewWikiPageService(repo, nil, nil, nil, nil, nil)
	ctx := context.Background()
	page, err := svc.CreatePage(ctx, &types.WikiPage{
		TenantID: 1, KnowledgeBaseID: "kb-retract-state", Slug: "concept/shared",
		Title: "Shared concept", Summary: "verified summary", Content: "verified body",
		PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished,
		SourceRefs:   types.StringArray{"source-a|A", "source-b|B"},
		ChunkRefs:    types.StringArray{"chunk-a", "chunk-b"},
		PageMetadata: types.JSON(`{"domain":"materials"}`),
	})
	require.NoError(t, err)
	staleSnapshot := *page

	types.SetWikiRetractionSource(page, "source-a", false)
	types.SetWikiRetractionSource(page, "source-b", false)
	page.SourceRefs = types.StringArray{"source-b|B"}
	page.ChunkRefs = types.StringArray{"chunk-b"}
	require.NoError(t, svc.UpdatePageMeta(types.WithWikiRetractionMetadataMutation(ctx), page))

	// A stale ordinary writer cannot reintroduce removed provenance or act on
	// an old marker snapshot.
	staleSnapshot.Content = "ordinary concurrent edit"
	staleSnapshot.PageMetadata = types.JSON(`{"domain":"revised"}`)
	_, err = svc.UpdatePage(ctx, &staleSnapshot)
	require.ErrorIs(t, err, ErrWikiPageStaleSnapshot)
	stored, err := svc.GetPageBySlug(ctx, "kb-retract-state", "concept/shared")
	require.NoError(t, err)
	require.Equal(t, types.StringArray{"source-b|B"}, stored.SourceRefs)
	require.Equal(t, types.StringArray{"chunk-b"}, stored.ChunkRefs)
	require.Equal(t, []string{"source-a", "source-b"}, types.ReadWikiRetractionState(stored).PendingSourceIDs)

	// A current ordinary writer preserves markers while replacing unrelated
	// metadata, and a reviewed write clears only the IDs it covered.
	stored.Content = "ordinary concurrent edit based on current page"
	stored.PageMetadata = types.JSON(`{"domain":"revised"}`)
	_, err = svc.UpdatePage(ctx, stored)
	require.NoError(t, err)
	stored, err = svc.GetPageBySlug(ctx, "kb-retract-state", "concept/shared")
	require.NoError(t, err)
	require.Equal(t, []string{"source-a", "source-b"}, types.ReadWikiRetractionState(stored).PendingSourceIDs)
	require.Contains(t, string(stored.PageMetadata), `"domain":"revised"`)

	// The reviewed retract explicitly clears only the IDs it covered.
	types.ClearWikiRetractionSources(stored, []string{"source-a"})
	_, err = svc.UpdatePage(types.WithWikiRetractionMetadataMutation(ctx), stored)
	require.NoError(t, err)
	stored, err = svc.GetPageBySlug(ctx, "kb-retract-state", "concept/shared")
	require.NoError(t, err)
	require.Equal(t, []string{"source-b"}, types.ReadWikiRetractionState(stored).PendingSourceIDs)
}
