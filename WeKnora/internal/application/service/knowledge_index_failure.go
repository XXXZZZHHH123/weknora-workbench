package service

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/api"
)

func batchIndexFailureCode(err error) string {
	var indexingErr *retriever.IndexingError
	if !errors.As(err, &indexingErr) || indexingErr.Phase != retriever.IndexingPhaseEmbedding {
		return werrors.ErrCodeVectorStoreWriteFailed
	}
	var httpErr *api.HTTPError
	if errors.As(indexingErr.Err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests {
		return werrors.ErrCodeEmbeddingRateLimit
	}
	return werrors.ErrCodeEmbeddingProviderFail
}
