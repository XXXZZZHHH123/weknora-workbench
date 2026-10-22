package chatpipeline

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// Phase two's claim is that memory changes what gets retrieved, not only what
// the answer prompt says. These tests hold the two places that has to be true:
// the query the retriever is given, and the order documents come back in.

type stubRetrievalMemory struct {
	stubMemoryService

	retrieval      interfaces.RetrievalContext
	affinity       map[string]int
	askedFor       []string
	retrievalCalls int
}

func (s *stubRetrievalMemory) RetrievalContextFor(context.Context) interfaces.RetrievalContext {
	s.retrievalCalls++
	return s.retrieval
}

func (s *stubRetrievalMemory) DocumentAffinity(_ context.Context, ids []string) map[string]int {
	s.askedFor = ids
	return s.affinity
}

func TestWhoIsAskingReachesTheQueryRewriter(t *testing.T) {
	memoryService := &stubRetrievalMemory{
		retrieval: interfaces.RetrievalContext{
			Background: "在做医学影像的后端",
			Interests:  []string{"医学影像分割"},
			Documents:  []string{"分割模型调参手册"},
			Items: []*types.MemoryItem{
				{ID: "m1", Kind: types.MemoryKindProfile, Content: "在做医学影像的后端"},
			},
		},
	}
	plugin := &PluginQueryUnderstand{
		memoryService: memoryService,
		config: &config.Config{Conversation: &config.ConversationConfig{
			RewritePromptSystem: "改写用户的问题。",
			RewritePromptUser:   "{{query}}",
		}},
	}

	chatManage := &types.ChatManage{}
	chatManage.Query = "分割怎么调参"
	chatManage.MemoryPrompt = types.WrapMemoryForPrompt("本轮相关背景：在做医学影像的后端", "")
	chatManage.UsedMemories = types.UsedMemories{{ID: "m1", Kind: types.MemoryKindProfile, Content: "在做医学影像的后端"}}

	_, userPrompt := plugin.buildPrompts(t.Context(), chatManage, nil)

	require.Contains(t, userPrompt, "在做医学影像的后端",
		"the same question means different things to different people, and only "+
			"the rewriter can act on that before retrieval runs")
	require.NotContains(t, userPrompt, "医学影像分割", "unselected standing interests must not influence this turn")
	require.NotContains(t, userPrompt, "分割模型调参手册", "unselected familiar documents must not influence this turn")
	require.Contains(t, userPrompt, "分割怎么调参", "the question itself must survive")

	require.Len(t, chatManage.UsedMemories, 1, "the rewriter reuses the same selected and reported memories")
	require.Zero(t, memoryService.retrievalCalls, "query understanding must not read raw standing background")
}

func TestQueryRewriterCannotInjectUnselectedStandingBackground(t *testing.T) {
	memory := &stubRetrievalMemory{retrieval: interfaces.RetrievalContext{Background: "unselected biography", Interests: []string{"unselected interest"}, Documents: []string{"unselected document"}}}
	plugin := &PluginQueryUnderstand{memoryService: memory, config: &config.Config{Conversation: &config.ConversationConfig{RewritePromptSystem: "Rewrite", RewritePromptUser: "{{query}}"}}}
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: "A separate current task"}}
	_, prompt := plugin.buildPrompts(t.Context(), cm, nil)
	require.NotContains(t, prompt, "asker_background")
	require.NotContains(t, prompt, "unselected")
	require.Zero(t, memory.retrievalCalls)
	require.Zero(t, memory.recallCall, "rewriting does not perform another memory selection")
}

func TestQueryRewriterIsUnchangedWithoutMemory(t *testing.T) {
	plugin := &PluginQueryUnderstand{
		memoryService: &stubRetrievalMemory{},
		config: &config.Config{Conversation: &config.ConversationConfig{
			RewritePromptSystem: "改写用户的问题。",
			RewritePromptUser:   "{{query}}",
		}},
	}
	chatManage := &types.ChatManage{}
	chatManage.Query = "分割怎么调参"

	_, userPrompt := plugin.buildPrompts(t.Context(), chatManage, nil)
	require.NotContains(t, userPrompt, "asker_background")
	require.Empty(t, chatManage.UsedMemories)
}

func TestFamiliarDocumentsRankHigher(t *testing.T) {
	memoryService := &stubRetrievalMemory{affinity: map[string]int{"doc-familiar": 8}}
	plugin := &PluginMemoryAffinity{memoryService: memoryService}

	chatManage := &types.ChatManage{
		PipelineState: types.PipelineState{RerankResult: []*types.SearchResult{
			{ID: "c1", KnowledgeID: "doc-stranger", Score: 0.80},
			{ID: "c2", KnowledgeID: "doc-familiar", Score: 0.78},
		}},
	}

	err := plugin.OnEvent(t.Context(), types.CHUNK_RERANK, chatManage, func() *PluginError {
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, "c2", chatManage.RerankResult[0].ID,
		"between two comparable passages, prefer the document this person works from")
}

func TestAnUnrelatedDocumentIsNotDraggedToTheTop(t *testing.T) {
	// The signal is weak — it says the retriever kept picking a document, not
	// that the user found it useful — so it must never overturn a clear
	// relevance gap.
	memoryService := &stubRetrievalMemory{affinity: map[string]int{"doc-familiar": 1000}}
	plugin := &PluginMemoryAffinity{memoryService: memoryService}

	chatManage := &types.ChatManage{
		PipelineState: types.PipelineState{RerankResult: []*types.SearchResult{
			{ID: "c1", KnowledgeID: "doc-relevant", Score: 0.90},
			{ID: "c2", KnowledgeID: "doc-familiar", Score: 0.40},
		}},
	}

	err := plugin.OnEvent(t.Context(), types.CHUNK_RERANK, chatManage, func() *PluginError {
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, "c1", chatManage.RerankResult[0].ID)
}

func TestRerankIsUntouchedWithoutAffinity(t *testing.T) {
	plugin := &PluginMemoryAffinity{memoryService: &stubRetrievalMemory{}}
	chatManage := &types.ChatManage{
		PipelineState: types.PipelineState{RerankResult: []*types.SearchResult{
			{ID: "c1", KnowledgeID: "doc-a", Score: 0.80},
			{ID: "c2", KnowledgeID: "doc-b", Score: 0.78},
		}},
	}
	err := plugin.OnEvent(t.Context(), types.CHUNK_RERANK, chatManage, func() *PluginError {
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, 0.80, chatManage.RerankResult[0].Score)
	require.Equal(t, 0.78, chatManage.RerankResult[1].Score)
}
