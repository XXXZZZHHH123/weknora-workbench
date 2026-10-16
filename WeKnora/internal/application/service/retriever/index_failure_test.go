package retriever

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
)

type failingPhaseEmbedder struct {
	embedding.Embedder
	err   error
	calls int
}

func (e *failingPhaseEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1}, e.err
}

func (e *failingPhaseEmbedder) BatchEmbedWithPool(_ context.Context, _ embedding.Embedder, texts []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = []float32{1}
	}
	return vectors, nil
}

type failingPhaseRepository struct {
	interfaces.RetrieveEngineRepository
	err   error
	calls atomic.Int64
}

func (r *failingPhaseRepository) Save(context.Context, *types.IndexInfo, map[string]any) error {
	r.calls.Add(1)
	return r.err
}

func (r *failingPhaseRepository) BatchSave(context.Context, []*types.IndexInfo, map[string]any) error {
	r.calls.Add(1)
	return r.err
}

func phaseIndexRows(n int) []*types.IndexInfo {
	rows := make([]*types.IndexInfo, n)
	for i := range rows {
		rows[i] = &types.IndexInfo{SourceID: fmt.Sprint(i), Content: "source passage"}
	}
	return rows
}

func TestIndexFailureIdentifiesEmbeddingWithoutWritingTheStore(t *testing.T) {
	for _, single := range []bool{true, false} {
		t.Run(fmt.Sprint("single=", single), func(t *testing.T) {
			cause := &api.HTTPError{StatusCode: http.StatusUnauthorized, Body: "credential rejected"}
			embedder := &failingPhaseEmbedder{err: cause}
			repo := &failingPhaseRepository{}
			service := &KeywordsVectorHybridRetrieveEngineService{indexRepository: repo}
			var err error
			if single {
				err = service.Index(context.Background(), embedder, phaseIndexRows(1)[0], []types.RetrieverType{types.VectorRetrieverType})
			} else {
				err = service.BatchIndex(context.Background(), embedder, phaseIndexRows(2), []types.RetrieverType{types.VectorRetrieverType})
			}
			require.ErrorIs(t, err, cause)
			require.ErrorContains(t, err, "embedding failed")
			var phaseErr *IndexingError
			require.ErrorAs(t, err, &phaseErr)
			require.Equal(t, IndexingPhaseEmbedding, phaseErr.Phase)
			require.EqualValues(t, 0, repo.calls.Load(), "the provider failed before any index write")
			require.Equal(t, 1, embedder.calls, "a permanent vendor rejection must not consume repeated calls")
		})
	}
}

func TestIndexFailureIdentifiesStoreWritesAcrossBatchPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   int
		vector bool
		single bool
	}{
		{"single vector", 1, true, true},
		{"concurrent vector", 41, true, false},
		{"bounded vector", 241, true, false},
		{"single keyword", 1, false, true},
		{"concurrent keyword", 11, false, false},
		{"bounded keyword", 51, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("storage quota exhausted")
			repo := &failingPhaseRepository{err: cause}
			embedder := &failingPhaseEmbedder{}
			service := &KeywordsVectorHybridRetrieveEngineService{indexRepository: repo}
			retrievers := []types.RetrieverType{types.KeywordsRetrieverType}
			if tc.vector {
				retrievers = append(retrievers, types.VectorRetrieverType)
			}
			var err error
			if tc.single {
				err = service.Index(context.Background(), embedder, phaseIndexRows(tc.rows)[0], retrievers)
			} else {
				err = service.BatchIndex(context.Background(), embedder, phaseIndexRows(tc.rows), retrievers)
			}
			require.ErrorIs(t, err, cause)
			require.ErrorContains(t, err, "vector store write failed")
			var phaseErr *IndexingError
			require.ErrorAs(t, err, &phaseErr)
			require.Equal(t, IndexingPhaseStoreWrite, phaseErr.Phase)
			require.Positive(t, repo.calls.Load())
			if tc.vector {
				require.Equal(t, 1, embedder.calls, "store writes failed after embedding succeeded")
			} else {
				require.Zero(t, embedder.calls)
			}
		})
	}
}

func TestBatchEmbeddingDoesNotRetryPermanentHTTPFailures(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			cause := &api.HTTPError{StatusCode: code, Body: "rejected"}
			embedder := &failingPhaseEmbedder{err: fmt.Errorf("upstream: %w", cause)}
			repo := &failingPhaseRepository{}
			service := &KeywordsVectorHybridRetrieveEngineService{indexRepository: repo}
			err := service.BatchIndex(context.Background(), embedder, phaseIndexRows(1), []types.RetrieverType{types.VectorRetrieverType})
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, embedder.calls)
			require.Zero(t, repo.calls.Load())
			var phaseErr *IndexingError
			require.ErrorAs(t, err, &phaseErr)
			require.Equal(t, IndexingPhaseEmbedding, phaseErr.Phase)
		})
	}
}

func TestBatchEmbeddingStopsOnCancellationAndSSRFRejection(t *testing.T) {
	for _, cause := range []error{
		context.Canceled, context.DeadlineExceeded,
		secutils.ErrSSRFHostNotWhitelisted, secutils.ErrSSRFRedirectBlocked,
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			embedder := &failingPhaseEmbedder{err: fmt.Errorf("request blocked: %w", cause)}
			repo := &failingPhaseRepository{}
			service := &KeywordsVectorHybridRetrieveEngineService{indexRepository: repo}
			err := service.BatchIndex(context.Background(), embedder, phaseIndexRows(1), []types.RetrieverType{types.VectorRetrieverType})
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, embedder.calls)
			require.Zero(t, repo.calls.Load())
		})
	}
}

func TestBatchEmbeddingLeavesTransientRetriesToProtocolLayer(t *testing.T) {
	for _, cause := range []error{
		&api.HTTPError{StatusCode: http.StatusTooManyRequests, Body: "slow down"},
		&api.HTTPError{StatusCode: http.StatusServiceUnavailable, Body: "unavailable"},
		&api.TransportError{Op: "send request", Err: errors.New("connection reset")},
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			embedder := &failingPhaseEmbedder{err: cause}
			repo := &failingPhaseRepository{}
			service := &KeywordsVectorHybridRetrieveEngineService{indexRepository: repo}
			err := service.BatchIndex(context.Background(), embedder, phaseIndexRows(1), []types.RetrieverType{types.VectorRetrieverType})
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, embedder.calls, "protocol requests own retries; never replay a whole embedding pool")
			require.Zero(t, repo.calls.Load())
			var phaseErr *IndexingError
			require.ErrorAs(t, err, &phaseErr)
			require.Equal(t, IndexingPhaseEmbedding, phaseErr.Phase)
		})
	}
}
