package chatpipeline

import (
	"context"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// wikiBoostFactor is the score multiplier applied to wiki page chunks.
// Wiki pages contain LLM-synthesized, cross-referenced knowledge and should
// be preferred over raw document chunks when both are available.
const wikiBoostFactor = 1.3

// PluginWikiBoost boosts the relevance score of wiki page chunks in search results.
// Wiki pages contain pre-synthesized knowledge that is more coherent and
// cross-referenced than raw document chunks, so they should rank higher.
//
// This plugin runs in the CHUNK_RERANK phase, after initial retrieval and reranking.
// It identifies chunks with ChunkType == "wiki_page" and multiplies their score.
type PluginWikiBoost struct {
	kbService   interfaces.KnowledgeBaseService
	wikiService interfaces.WikiPageService
}

// NewPluginWikiBoost creates and registers the wiki boost plugin
func NewPluginWikiBoost(eventManager *EventManager, kbService interfaces.KnowledgeBaseService, wikiService interfaces.WikiPageService) *PluginWikiBoost {
	p := &PluginWikiBoost{
		kbService:   kbService,
		wikiService: wikiService,
	}
	eventManager.Register(p)
	return p
}

// ActivationEvents returns the event types this plugin handles
func (p *PluginWikiBoost) ActivationEvents() []types.EventType {
	return []types.EventType{types.CHUNK_RERANK}
}

// OnEvent boosts wiki page chunk scores after reranking
func (p *PluginWikiBoost) OnEvent(
	ctx context.Context,
	eventType types.EventType,
	chatManage *types.ChatManage,
	next func() *PluginError,
) *PluginError {
	// Run the normal reranking first
	if err := next(); err != nil {
		return err
	}

	// The legacy chat retriever can still surface a synchronized Wiki chunk
	// directly, bypassing the Wiki tools. Strip marked or unresolvable Wiki
	// chunks from every candidate slice before downstream context assembly.
	var withheldSearch, withheldRerank, withheldMerge int
	chatManage.SearchResult, withheldSearch = p.filterWikiChunks(ctx, chatManage.SearchResult, chatManage.SearchTargets)
	chatManage.RerankResult, withheldRerank = p.filterWikiChunks(ctx, chatManage.RerankResult, chatManage.SearchTargets)
	chatManage.MergeResult, withheldMerge = p.filterWikiChunks(ctx, chatManage.MergeResult, chatManage.SearchTargets)
	if withheld := withheldSearch + withheldRerank + withheldMerge; withheld > 0 {
		logger.Warnf(ctx, "WikiBoost: withheld %d retracted or unverified Wiki chunk candidates", withheld)
	}

	// Fast path: skip all work if there are no wiki_page chunks in the result set.
	// This avoids hitting the KB service on every chat turn for non-wiki queries.
	hasWikiChunk := false
	for i := range chatManage.RerankResult {
		if chatManage.RerankResult[i].ChunkType == types.ChunkTypeWikiPage {
			hasWikiChunk = true
			break
		}
	}
	if !hasWikiChunk {
		return nil
	}

	// Confirm at least one search target is actually a wiki KB.
	hasWikiKB := false
	for _, target := range chatManage.SearchTargets {
		if target == nil || target.KnowledgeBaseID == "" {
			continue
		}
		kb, err := p.kbService.GetKnowledgeBaseByIDOnly(ctx, target.KnowledgeBaseID)
		if err == nil && kb != nil && kb.IsWikiEnabled() {
			hasWikiKB = true
			break
		}
	}
	if !hasWikiKB {
		return nil
	}

	// Boost wiki page chunks in RerankResult
	boostedCount := 0
	for i := range chatManage.RerankResult {
		if chatManage.RerankResult[i].ChunkType == types.ChunkTypeWikiPage {
			chatManage.RerankResult[i].Score *= wikiBoostFactor
			boostedCount++
		}
	}

	if boostedCount > 0 {
		logger.Infof(ctx, "WikiBoost: boosted %d wiki page chunks by %.1fx", boostedCount, wikiBoostFactor)
		// Re-sort by score after boosting; stable sort preserves ordering for ties.
		sort.SliceStable(chatManage.RerankResult, func(i, j int) bool {
			return chatManage.RerankResult[i].Score > chatManage.RerankResult[j].Score
		})
	}

	return nil
}

func (p *PluginWikiBoost) filterWikiChunks(ctx context.Context, results []*types.SearchResult, targets types.SearchTargets) ([]*types.SearchResult, int) {
	visible := make([]*types.SearchResult, 0, len(results))
	withheld := 0
	verifiedPages := make(map[string]bool)
	for _, result := range results {
		if result == nil {
			continue
		}
		if result.ChunkType != string(types.ChunkTypeWikiPage) && !strings.HasPrefix(result.ID, "wp-") {
			visible = append(visible, result)
			continue
		}
		if p.wikiService == nil || !strings.HasPrefix(result.ID, "wp-") || len(result.ID) == len("wp-") {
			withheld++
			continue
		}
		pageID := strings.TrimPrefix(result.ID, "wp-")
		cacheKey := pageID + "\x00" + result.KnowledgeBaseID
		verified, checked := verifiedPages[cacheKey]
		if !checked {
			page, err := p.wikiService.GetPageByID(ctx, pageID)
			state := types.ReadWikiRetractionState(page)
			expectedTenant := uint64(0)
			for _, target := range targets {
				if target != nil && target.KnowledgeBaseID == result.KnowledgeBaseID {
					expectedTenant = target.TenantID
					break
				}
			}
			verified = err == nil && page != nil && page.ID == pageID &&
				page.KnowledgeBaseID == result.KnowledgeBaseID &&
				(expectedTenant == 0 || page.TenantID == expectedTenant) &&
				len(state.PendingSourceIDs) == 0 && len(state.FailedSourceIDs) == 0
			verifiedPages[cacheKey] = verified
		}
		if !verified {
			withheld++
			continue
		}
		visible = append(visible, result)
	}
	return visible, withheld
}
