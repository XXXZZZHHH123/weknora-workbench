package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

func chunkContentView(chunk *types.Chunk, ranges []types.ChunkRuneRange) *types.ChunkContentView {
	if chunk == nil || len(ranges) == 0 || len(ranges) > types.MaxChunkContentViewRanges {
		return nil
	}
	digest := sha256.Sum256([]byte(chunk.Content))
	return &types.ChunkContentView{OriginSHA256: hex.EncodeToString(digest[:]), OriginRevision: chunk.ContentRevision,
		OriginRuneCount: utf8.RuneCountInString(chunk.Content), Ranges: append([]types.ChunkRuneRange(nil), ranges...)}
}

// Keep an unchanged full-body presentation on the original citation
// protocol. Its internal ranges still compose with subsequent pagination.
func identityChunkContentView(chunk *types.Chunk, view *types.ChunkContentView) bool {
	if chunk == nil || view == nil || view.OriginRevision != chunk.ContentRevision ||
		view.OriginRuneCount != utf8.RuneCountInString(chunk.Content) || len(view.Ranges) == 0 || len(view.Ranges) > types.MaxChunkContentViewRanges {
		return false
	}
	digest := sha256.Sum256([]byte(chunk.Content))
	if view.OriginSHA256 != hex.EncodeToString(digest[:]) {
		return false
	}
	end := 0
	for _, span := range view.Ranges {
		if span.Start != end || span.End <= span.Start || span.End > view.OriginRuneCount {
			return false
		}
		end = span.End
	}
	return end == view.OriginRuneCount
}

// Compose content pagination with a prior page projection. Never rebase the
// origin hash or offsets onto the already clipped page body.
func sliceChunkContentView(view *types.ChunkContentView, start, end int) *types.ChunkContentView {
	if view == nil || start < 0 || end <= start {
		return nil
	}
	ranges := []types.ChunkRuneRange{}
	position := 0
	for _, source := range view.Ranges {
		next := position + source.End - source.Start
		left, right := max(start, position), min(end, next)
		if left < right {
			ranges = append(ranges, types.ChunkRuneRange{Start: source.Start + left - position, End: source.Start + right - position})
		}
		position = next
	}
	if end > position || len(ranges) == 0 || len(ranges) > types.MaxChunkContentViewRanges {
		return nil
	}
	copy := *view
	copy.Ranges = ranges
	return &copy
}
