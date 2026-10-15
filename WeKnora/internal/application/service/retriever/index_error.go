package retriever

import "fmt"

type IndexingPhase string

const (
	IndexingPhaseEmbedding  IndexingPhase = "embedding"
	IndexingPhaseStoreWrite IndexingPhase = "vector store write"
)

// IndexingError identifies which side of indexing failed without guessing
// from a provider's or database's message. The original error remains usable
// with errors.Is/As, including its HTTP status or transport cause.
type IndexingError struct {
	Phase IndexingPhase
	Err   error
}

func (e *IndexingError) Error() string { return fmt.Sprintf("%s failed: %v", e.Phase, e.Err) }
func (e *IndexingError) Unwrap() error { return e.Err }

func indexWriteError(err error) error {
	if err == nil {
		return nil
	}
	return &IndexingError{Phase: IndexingPhaseStoreWrite, Err: err}
}
