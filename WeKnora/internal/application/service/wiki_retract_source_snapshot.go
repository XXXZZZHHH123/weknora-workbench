package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

func wikiRetractTextDigest(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func wikiRetractSourceReadable(source *types.Knowledge) bool {
	return source != nil && (source.ParseStatus == types.ParseStatusCompleted || source.ParseStatus == types.ParseStatusFinalizing)
}

func wikiRetractDigestsMatchEvidence(digests []wikiRetractChunkDigest, evidence []wikiRetractSourceEvidence) bool {
	want := []wikiRetractChunkDigest{}
	for _, source := range evidence {
		want = append(want, source.Snapshot...)
	}
	canonical := func(rows []wikiRetractChunkDigest) string {
		copyRows := append([]wikiRetractChunkDigest(nil), rows...)
		sort.Slice(copyRows, func(i, j int) bool {
			if copyRows[i].SourceID != copyRows[j].SourceID {
				return copyRows[i].SourceID < copyRows[j].SourceID
			}
			return copyRows[i].ChunkID < copyRows[j].ChunkID
		})
		encoded, _ := json.Marshal(copyRows)
		return string(encoded)
	}
	return len(want) > 0 && canonical(want) == canonical(digests)
}

// Recheck actual stored observations at the commit boundary. A completed model
// review cannot authorize a changed chunk, source reparse or concurrent delete.
func (s *wikiIngestService) validateWikiRetractEvidenceSnapshot(ctx context.Context, kbID string, tenantID uint64, evidence []wikiRetractSourceEvidence) error {
	actualTenant, scoped := types.TenantIDFromContext(ctx)
	if !scoped || actualTenant != tenantID || s.knowledgeSvc == nil || s.chunkRepo == nil || len(evidence) == 0 {
		return errors.New("wiki retract: source snapshot validation is unavailable or out of scope")
	}
	seenSources := map[string]bool{}
	for _, source := range evidence {
		if source.SourceID == "" || seenSources[source.SourceID] || len(source.Snapshot) == 0 {
			return errors.New("wiki retract: incomplete or duplicate source snapshot")
		}
		seenSources[source.SourceID] = true
		knowledge, err := s.knowledgeSvc.GetKnowledgeByIDOnly(ctx, source.SourceID)
		if err != nil || !wikiRetractSourceReadable(knowledge) || knowledge.ID != source.SourceID || knowledge.TenantID != tenantID || knowledge.KnowledgeBaseID != kbID {
			return errors.New("wiki retract: surviving source changed or is no longer readable")
		}
		chunks, err := s.chunkRepo.ListChunksByKnowledgeID(ctx, tenantID, source.SourceID)
		if err != nil {
			return errors.New("wiki retract: could not revalidate source chunks")
		}
		current := map[string]*types.Chunk{}
		for _, chunk := range chunks {
			if chunk == nil || chunk.ID == "" || chunk.Content == "" {
				continue
			}
			if chunk.TenantID != tenantID || chunk.KnowledgeBaseID != kbID || chunk.KnowledgeID != source.SourceID {
				return errors.New("wiki retract: source chunk ownership changed")
			}
			if chunk.ChunkType != "" && chunk.ChunkType != types.ChunkTypeText {
				continue
			}
			if current[chunk.ID] != nil {
				return errors.New("wiki retract: duplicate source chunk identity")
			}
			current[chunk.ID] = chunk
		}
		if source.EvidenceScope == "full_document_fallback" && len(current) != len(source.Snapshot) {
			return errors.New("wiki retract: full source chunk set changed")
		}
		seenChunks := map[string]bool{}
		for _, digest := range source.Snapshot {
			chunk := current[digest.ChunkID]
			if chunk == nil || seenChunks[digest.ChunkID] || digest.SourceID != source.SourceID || chunk.ChunkIndex != digest.Index {
				return errors.New("wiki retract: observed source chunk identity changed")
			}
			seenChunks[digest.ChunkID] = true
			content := reconstructEnrichedContent(ctx, s.chunkRepo, tenantID, []*types.Chunk{chunk})
			if utf8.RuneCountInString(content) != digest.RuneLen || wikiRetractTextDigest(content) != digest.SHA256 {
				return errors.New("wiki retract: observed source chunk content changed")
			}
		}
	}
	return ctx.Err()
}
