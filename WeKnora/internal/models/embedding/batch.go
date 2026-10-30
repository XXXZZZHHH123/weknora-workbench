package embedding

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/Tencent/WeKnora/internal/models/utils"
	"github.com/panjf2000/ants/v2"
)

type batchEmbedder struct {
	pool *ants.Pool
}

func NewBatchEmbedder(pool *ants.Pool) EmbedderPooler {
	return &batchEmbedder{pool: pool}
}

type textEmbedding struct {
	text    string
	results []float32
}

func (e *batchEmbedder) BatchEmbedWithPool(ctx context.Context, model Embedder, texts []string) ([][]float32, error) {
	// Create goroutine pool for concurrent processing of document chunks
	var wg sync.WaitGroup
	var mu sync.Mutex  // For synchronizing access to error
	var firstErr error // Record the first error that occurs
	batchSizeStr := os.Getenv("BATCH_EMBED_SIZE")
	if batchSizeStr == "" {
		batchSizeStr = "5"
	}
	batchSize, err := strconv.Atoi(batchSizeStr)
	if err != nil || batchSize <= 0 {
		return nil, fmt.Errorf("BATCH_EMBED_SIZE must be a positive integer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if e.pool == nil {
		return nil, fmt.Errorf("embedding worker pool is not configured")
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	setError := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	textEmbeddings := utils.MapSlice(texts, func(text string) *textEmbedding {
		return &textEmbedding{text: text}
	})

	// Function to process each document chunk
	processChunk := func(texts []*textEmbedding) func() {
		return func() {
			defer wg.Done()
			// If an error has already occurred, don't continue processing. The
			// read takes mu for the same reason the writes below do: every pool
			// worker evaluates this guard, so an unsynchronised read races with
			// another worker's write. Beyond the detector, a worker that misses
			// the write bills another provider request after the run has already
			// failed, which is exactly what this guard exists to prevent.
			mu.Lock()
			aborted := firstErr != nil
			mu.Unlock()
			if aborted || batchCtx.Err() != nil {
				return
			}
			// Embed text
			embedding, err := model.BatchEmbed(batchCtx, utils.MapSlice(texts, func(text *textEmbedding) string {
				return text.text
			}))
			if err != nil {
				setError(err)
				return
			}
			if len(embedding) != len(texts) {
				setError(fmt.Errorf("embedding model returned %d embeddings for %d inputs", len(embedding), len(texts)))
				return
			}
			mu.Lock()
			for i, text := range texts {
				if text == nil {
					continue
				}
				text.results = embedding[i]
			}
			mu.Unlock()
		}
	}

	// Submit all tasks to the goroutine pool
	for _, texts := range utils.ChunkSlice(textEmbeddings, batchSize) {
		if batchCtx.Err() != nil {
			break
		}
		wg.Add(1)
		err := e.pool.Submit(processChunk(texts))
		if err != nil {
			wg.Done()
			setError(err)
			break
		}
	}

	// Wait for all tasks to complete
	wg.Wait()

	// Check if any errors occurred
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	results := utils.MapSlice(textEmbeddings, func(text *textEmbedding) []float32 {
		return text.results
	})
	return results, nil
}
