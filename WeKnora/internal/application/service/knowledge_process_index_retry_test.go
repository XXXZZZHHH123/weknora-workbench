package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type indexRetryKnowledgeRepo struct {
	parentChildKnowledgeRepo
	updates []types.Knowledge
}

func (r *indexRetryKnowledgeRepo) UpdateKnowledge(_ context.Context, k *types.Knowledge) error {
	r.updates = append(r.updates, *k)
	return nil
}

type indexRetryChunkRepo struct {
	parentChildChunkService
	deletes int
}

func (s *indexRetryChunkRepo) DeleteChunksByKnowledgeID(context.Context, uint64, string) error {
	s.deletes++
	return nil
}

type failingIndexEngine struct {
	parentChildRetrieveEngine
	err          error
	indexDeletes int
}

func (e *failingIndexEngine) BatchIndex(
	context.Context, embedding.Embedder, []*types.IndexInfo, []types.RetrieverType,
) error {
	return e.err
}

func (e *failingIndexEngine) DeleteByKnowledgeIDList(context.Context, []string, int, string) error {
	e.indexDeletes++
	return nil
}

func runProcessChunksWithFailingIndex(
	ctx context.Context, t *testing.T, tracker SpanTracker,
) (*indexRetryKnowledgeRepo, *indexRetryChunkRepo, *failingIndexEngine, error) {
	return runProcessChunksWithIndexCause(ctx, t, errors.New("embedding endpoint unavailable"), tracker)
}

func runProcessChunksWithIndexCause(
	ctx context.Context, t *testing.T, cause error, tracker SpanTracker,
) (*indexRetryKnowledgeRepo, *indexRetryChunkRepo, *failingIndexEngine, error) {
	t.Helper()
	knowledge := &types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusProcessing,
	}
	repo := &indexRetryKnowledgeRepo{parentChildKnowledgeRepo: parentChildKnowledgeRepo{knowledge: knowledge}}
	chunks := &indexRetryChunkRepo{}
	engine := &failingIndexEngine{err: cause}
	tenant := &types.Tenant{
		ID: 1,
		RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{{
			RetrieverType:       types.VectorRetrieverType,
			RetrieverEngineType: types.PostgresRetrieverEngineType,
		}}},
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	svc := &knowledgeService{
		repo:           repo,
		spanTracker:    tracker,
		chunkRepo:      chunks,
		modelService:   parentChildModelService{embedder: parentChildEmbedder{}},
		retrieveEngine: parentChildRetrieveRegistry{engine: engine},
		graphEngine:    parentChildGraphRepo{},
		tenantRepo:     parentChildTenantRepo{},
		task:           parentChildTaskEnqueuer{},
	}
	kb := &types.KnowledgeBase{
		ID:               "kb-1",
		TenantID:         1,
		EmbeddingModelID: "embedding-1",
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
	}
	err := svc.processChunks(ctx, kb, knowledge, []types.ParsedChunk{
		{Content: "document body", Seq: 0, Start: 0, End: 13, ParentIndex: -1},
	})
	return repo, chunks, engine, err
}

// A transient embedding or vector-store outage must reach the task queue while
// retries remain. Acking it here would mark the document failed after the
// first attempt and leave the configured document-task retries unused.
func TestProcessChunksRetriesBatchIndexFailureBeforeLastAttempt(t *testing.T) {
	ctx := types.WithTaskRetryMetadata(context.Background(), 0, 3)

	tracker, db := setupSpanTrackerTest(t)
	_, attempt, err := tracker.OpenAttempt(ctx, "knowledge-1", "")
	require.NoError(t, err)
	ctx = withAttempt(ctx, attempt)
	repo, chunks, engine, err := runProcessChunksWithFailingIndex(ctx, t, tracker)

	require.ErrorContains(t, err, "embedding endpoint unavailable")
	status, code, message := rootSpan(t, db, "knowledge-1", attempt)
	require.Equal(t, types.SpanStatusRunning, status)
	require.Empty(t, code)
	require.Empty(t, message)
	for _, update := range repo.updates {
		require.NotEqual(t, types.ParseStatusFailed, update.ParseStatus)
	}
	// Partial chunks and vectors are still removed; the retry rebuilds them.
	require.Equal(t, 2, chunks.deletes)
	require.Equal(t, 2, engine.indexDeletes)
	require.Zero(t, chunks.purges, "a failed index must retain soft-deleted history for recovery")
}

func TestProcessChunksFailsKnowledgeOnLastBatchIndexAttempt(t *testing.T) {
	ctx := types.WithTaskRetryMetadata(context.Background(), 3, 3)

	tracker, db := setupSpanTrackerTest(t)
	_, attempt, err := tracker.OpenAttempt(ctx, "knowledge-1", "")
	require.NoError(t, err)
	ctx = withAttempt(ctx, attempt)
	repo, chunks, engine, err := runProcessChunksWithFailingIndex(ctx, t, tracker)

	require.NoError(t, err)
	status, code, message := rootSpan(t, db, "knowledge-1", attempt)
	require.Equal(t, types.SpanStatusFailed, status)
	require.Equal(t, werrors.ErrCodeVectorStoreWriteFailed, code)
	require.Equal(t, "batch index failed", message)
	require.NotEmpty(t, repo.updates)
	last := repo.updates[len(repo.updates)-1]
	require.Equal(t, types.ParseStatusFailed, last.ParseStatus)
	require.Equal(t, "embedding endpoint unavailable", last.ErrorMessage)
	require.Equal(t, 2, chunks.deletes)
	require.Equal(t, 2, engine.indexDeletes)
	require.Zero(t, chunks.purges, "a terminal index failure must retain soft-deleted history for recovery")
}

func TestProcessChunksRecordsTheActualIndexFailurePhase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase retriever.IndexingPhase
		cause error
		code  string
	}{
		{"provider auth", retriever.IndexingPhaseEmbedding, &api.HTTPError{StatusCode: http.StatusUnauthorized, Body: "credential rejected"}, werrors.ErrCodeEmbeddingProviderFail},
		{"provider invalid dimension", retriever.IndexingPhaseEmbedding, &api.HTTPError{StatusCode: http.StatusBadRequest, Body: "invalid dimension"}, werrors.ErrCodeEmbeddingProviderFail},
		{"provider rate limit", retriever.IndexingPhaseEmbedding, &api.HTTPError{StatusCode: http.StatusTooManyRequests, Body: "slow down"}, werrors.ErrCodeEmbeddingRateLimit},
		{"provider transport", retriever.IndexingPhaseEmbedding, &api.TransportError{Op: "send request", Err: errors.New("connection reset")}, werrors.ErrCodeEmbeddingProviderFail},
		{"provider text mentioning quota and 429", retriever.IndexingPhaseEmbedding, &api.HTTPError{StatusCode: http.StatusBadRequest, Body: "quota configuration refers to code 429"}, werrors.ErrCodeEmbeddingProviderFail},
		{"store quota", retriever.IndexingPhaseStoreWrite, errors.New("storage quota exhausted"), werrors.ErrCodeVectorStoreWriteFailed},
		{"store HTTP rate limit", retriever.IndexingPhaseStoreWrite, &api.HTTPError{StatusCode: http.StatusTooManyRequests, Body: "vector backend limit"}, werrors.ErrCodeVectorStoreWriteFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker, db := setupSpanTrackerTest(t)
			ctx := types.WithTaskRetryMetadata(context.Background(), 3, 3)
			_, attempt, err := tracker.OpenAttempt(ctx, "knowledge-1", "")
			require.NoError(t, err)
			ctx = withAttempt(ctx, attempt)
			cause := fmt.Errorf("composite index: %w", &retriever.IndexingError{Phase: tc.phase, Err: tc.cause})
			repo, chunks, engine, err := runProcessChunksWithIndexCause(ctx, t, cause, tracker)
			require.NoError(t, err)
			require.Equal(t, types.ParseStatusFailed, repo.updates[len(repo.updates)-1].ParseStatus)
			require.Equal(t, cause.Error(), repo.updates[len(repo.updates)-1].ErrorMessage)
			require.Equal(t, 2, chunks.deletes)
			require.Equal(t, 2, engine.indexDeletes)
			var span types.KnowledgeProcessingSpan
			require.NoError(t, db.Where("knowledge_id = ? AND attempt = ? AND name = ?", "knowledge-1", attempt, types.StageEmbedding).First(&span).Error)
			require.Equal(t, tc.code, span.ErrorCode)
			require.Contains(t, span.ErrorDetail, tc.cause.Error(), "operators retain the underlying provider or store error")
		})
	}
}
