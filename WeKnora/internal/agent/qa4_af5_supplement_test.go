package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// The assistant's Af4 plan and structured returns come from its recorded
// turns. Bodies are fetched by the real reader from the same-snapshot chunks
// listed in the successful document receipts. The keyword hits are the raw
// ranked SQLite matches from that snapshot, before agent deduplication.
type af5SupplementFixture struct {
	Query     string
	Traces    []af5SupplementTrace
	Documents []*types.Knowledge
	Chunks    []*types.Chunk
	Hits      []*types.SearchResult
}

type af5SupplementTrace struct {
	ID, Question, Answer string
	Plan                 *taskEvidencePlan
	Libraries            []string
	Steps                []types.AgentStep
}

type af5SupplementIndex struct {
	sourceDiscoveryKnowledgeService
	t        *testing.T
	library  string
	query    string
	hits     []*types.SearchResult
	reversed bool
	calls    int
}

func (i *af5SupplementIndex) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	i.t.Error("local recall must not prepare embedding model identities")
	return nil
}

func (i *af5SupplementIndex) HybridSearch(_ context.Context, id string, params types.SearchParams) ([]*types.SearchResult, error) {
	i.calls++
	if id != i.library || !slices.Equal(params.KnowledgeBaseIDs, []string{i.library}) || params.QueryText != i.query ||
		!params.DisableVectorMatch || params.DisableKeywordsMatch || len(params.QueryEmbedding) != 0 {
		i.t.Errorf("unexpected keyword index query: %s %+v", id, params)
		return nil, fmt.Errorf("unexpected keyword index query")
	}
	n := min(params.MatchCount, len(i.hits))
	hits := make([]*types.SearchResult, 0, n)
	for _, raw := range i.hits[:n] {
		hit := *raw
		// The production keyword-only search divides by the best raw BM25
		// score and applies the primary MatchCount after de-duplication.
		hit.Score /= i.hits[0].Score
		hits = append(hits, &hit)
	}
	if i.reversed {
		slices.Reverse(hits)
	}
	return hits, nil
}

type af5SupplementNoRerank struct {
	rerank.Reranker
	t *testing.T
}

func (r af5SupplementNoRerank) Rerank(context.Context, string, []string) ([]rerank.RankResult, error) {
	r.t.Error("local recall must not call a reranker")
	return nil, fmt.Errorf("unexpected rerank")
}

func loadAf5SupplementFixture(t *testing.T) af5SupplementFixture {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/af5-kb31.json.gz")
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer reader.Close()
	var f af5SupplementFixture
	require.NoError(t, json.NewDecoder(reader).Decode(&f))
	return f
}

func af5SupplementEngine(t *testing.T, f af5SupplementFixture, trace af5SupplementTrace, model *mockChat, reversed bool) (*AgentEngine, *af5SupplementIndex) {
	t.Helper()
	e := newTestEngine(t, model, withCitationsEnabled(true), withMaxIterations(20))
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.config.EvidenceReferencesEnabled = true
	require.Len(t, trace.Libraries, 1)
	library := trace.Libraries[0]
	e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
	e.toolRegistry = agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool().WithSearchTargets(e.config.SearchTargets)
	e.toolRegistry.RegisterTool(base)
	e.toolRegistry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, operation := range sourceNavigationRows(manifest["tools"]) {
		if operation["name"] == "query_series" {
			coverageObject(coverageObject(operation["inputSchema"])["properties"])["include_last_valid"] = map[string]any{"type": "boolean"}
		}
	}
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, manifest) {
		e.toolRegistry.RegisterTool(tool)
	}
	docs := &qa4Stage2CDocuments{documents: f.Documents}
	chunks := &qa4Stage2CChunks{repo: &qa4Stage2CChunkRepository{rows: f.Chunks}}
	index := &af5SupplementIndex{t: t, library: library, query: f.Query, hits: f.Hits, reversed: reversed}
	e.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(index, docs, chunks, e.config.SearchTargets, af5SupplementNoRerank{t: t}, nil))
	e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(docs, chunks, e.config.SearchTargets))
	agenttools.PrepareDocumentScope(e.toolRegistry, trace.Question)
	e.taskPlan = trace.Plan
	return e, index
}

func TestQA4Af5SupplementReadsOriginalPlan(t *testing.T) {
	for _, mode := range []string{"normal", "budget", "reversed", "located", "disabled", "only", "revoked", "spent", "time limit", "round limit", "already read"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "false")
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "true")
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "false")
			flag := strconv.FormatBool(mode != "disabled")
			if mode == "normal" {
				flag = "" // Default behavior with no FromEnv override.
			}
			t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", flag)
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
			t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			f := loadAf5SupplementFixture(t)
			require.Len(t, f.Traces, 3)
			for i, trace := range f.Traces {
				historicalRead := i == 1
				if historicalRead != (mode == "already read") {
					continue
				}
				t.Run(trace.ID, func(t *testing.T) {
					model := &mockChat{}
					e, index := af5SupplementEngine(t, f, trace, model, mode == "reversed")
					ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "fixture", Role: types.TenantRoleViewer}))
					ctx = context.WithValue(ctx, types.LanguageContextKey, "zh-CN")
					state := &types.AgentState{}
					messages := []chat.Message{{Role: "user", Content: trace.Question}}
					e.turnReview.Query, e.turnReview.Conversation = trace.Question, messages
					e.reviewConversation, e.reviewConversationReady = conversationThroughLastHuman(messages), true

					for _, step := range trace.Steps {
						for j := range step.ToolCalls {
							call := &step.ToolCalls[j]
							raw, err := json.Marshal(call.Args)
							require.NoError(t, err)
							if call.Name == agenttools.ToolBindDocumentScope || call.Name == agenttools.ToolReadDocument && call.Result.Success {
								tool, err := e.toolRegistry.GetTool(call.Name)
								require.NoError(t, err)
								result, err := tool.Execute(ctx, raw)
								require.NoError(t, err)
								require.True(t, result.Success, result.Error)
								if call.Name == agenttools.ToolReadDocument {
									require.Equal(t, call.Result.Data["knowledge_id"], result.Data["knowledge_id"])
									require.EqualValues(t, call.Result.Data["page"], citationInt(result.Data["page"]))
								}
								call.Result = result
							}
							if descriptor, _, native := e.toolRegistry.StructuredInvocation(call.Name, raw); native {
								call.Result.Data["_structured_execution"] = descriptor
							}
						}
						state.CurrentRound = max(state.CurrentRound, step.Iteration)
						state.RoundSteps = append(state.RoundSteps, step)
					}
					if mode == "reversed" {
						slices.Reverse(state.RoundSteps)
						for _, step := range state.RoundSteps {
							slices.Reverse(step.ToolCalls)
						}
					}
					for _, step := range state.RoundSteps {
						messages = e.appendToolResults(messages, step)
					}
					e.turnReview.Conversation = messages
					e.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
					archived, _ := e.terminalCoverageRecord(ctx, state)
					require.Equal(t, historicalRead, slices.ContainsFunc(archived.Evidence, func(item coverageEvidence) bool {
						return item.Kind == "body" && item.SourceDomain == "knowledge" && item.Page == 18
					}), "the historical r1 and r3 traces have no document body")
					if !historicalRead {
						require.Contains(t, strings.ReplaceAll(trace.Answer, ",", ""), "5160")
						require.NotContains(t, trace.Answer, "60.6")
						gap, found := e.finalSupplementPointGap(ctx, archived)
						require.True(t, found, "the executed complete lookup identified the planned point gap")
						require.Equal(t, f.Query, gap.metric+" "+gap.period)
					}
					switch mode {
					case "located":
						// The same authorized directory was observed in r2.
						// Add only that locator, without any report read.
						for _, step := range f.Traces[1].Steps {
							if len(step.ToolCalls) == 1 && step.ToolCalls[0].Name == agenttools.ToolSearchKnowledge {
								state.RoundSteps = append(state.RoundSteps, step)
								require.True(t, step.ToolCalls[0].Result.Success)
								break
							}
						}
					case "only":
						target := &e.taskPlan.CoverageTargets[0]
						target.SourceMode, target.PrimaryRef, target.RestrictionBasis = "only", "棉花数据库", trace.Question
					case "revoked":
						ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"another-library"}})
					case "spent":
						e.turnReview.FinalCoverageReadAttempted = true
					case "time limit":
						ctx = context.WithValue(ctx, agentExecutionStartKey{}, time.Now().Add(-standardAnswerActiveBudget-time.Second))
					case "round limit":
						state.CurrentRound = e.config.MaxIterations - 3
					}
					start := len(state.RoundSteps)
					candidate := trace.Answer
					if !historicalRead {
						// The stop path must deliver both facts even if the last
						// candidate had only the worksheet.
						candidate = strings.ReplaceAll(candidate, "5,160", "5160")
					}
					if mode == "budget" {
						// The active lookup budget has elapsed but the reserved
						// final synthesis is still available. Replay only the
						// existing final attempts, with no extra model response.
						ctx = context.WithValue(ctx, agentExecutionStartKey{}, time.Now().Add(-standardAnswerActiveBudget-time.Second))
						model.responses = []mockResponse{chatAnswer(candidate), chatAnswer(candidate), chatAnswer(candidate)}
						finished, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "session", "turn", &messages, &loopGuards{}, "active_time")
						require.NoError(t, err)
						require.True(t, finished)
						require.NotEmpty(t, model.calls)
						require.LessOrEqual(t, model.callCount, 3)
						require.True(t, slices.ContainsFunc(model.calls[0], func(m chat.Message) bool {
							return m.Role == "tool" && m.Name == agenttools.ToolReadDocument && strings.Contains(m.Content, "60.6")
						}), "the first existing synthesis saw the newly read page body in a tool message")
					} else {
						e.commitTerminalResult(ctx, state, "session", candidate, "", "answer", false)
						require.Zero(t, model.callCount, "reserved native recovery has no model call")
					}

					wantNew := mode == "normal" || mode == "budget" || mode == "reversed" || mode == "located"
					var newReads []types.ToolCall
					for _, step := range state.RoundSteps[start:] {
						newReads = append(newReads, step.ToolCalls...)
					}
					if wantNew {
						if mode != "located" {
							require.Equal(t, 1, index.calls)
							require.Len(t, newReads, 4)
							require.Equal(t, agenttools.ToolSearchKnowledge, newReads[0].Name)
							newReads = newReads[1:]
						} else {
							require.Zero(t, index.calls)
							require.Len(t, newReads, 3)
						}
						require.Equal(t, agenttools.ToolBindDocumentScope, newReads[0].Name)
						require.Equal(t, agenttools.ToolReadDocument, newReads[1].Name)
						require.Equal(t, agenttools.ToolReadDocument, newReads[2].Name)
						require.NotEqual(t, newReads[1].Args["id"], newReads[2].Args["id"], "same-date versions remain separate")
						for _, read := range newReads[1:] {
							require.True(t, read.Result.Success, read.Result.Error)
							require.Equal(t, 18, citationInt(read.Result.Data["page"]))
						}
					} else {
						require.Zero(t, index.calls)
						require.Empty(t, newReads)
					}
					var complete event.AgentCompleteData
					e.eventBus.On(event.EventAgentComplete, func(_ context.Context, published event.Event) error {
						complete = published.Data.(event.AgentCompleteData)
						return nil
					})
					e.emitCompletionEvent(ctx, state, "session", "turn", time.Now())
					require.Equal(t, complete.FinalAnswer, state.FinalAnswer)
					require.NotEmpty(t, state.FinalAnswer)
					require.NotContains(t, state.FinalAnswer, "<kb")
					require.NotContains(t, state.FinalAnswer, "<evidence")
					if mode != "revoked" && mode != "only" {
						require.Contains(t, state.FinalAnswer, "5160")
						require.Contains(t, state.FinalAnswer, "B3340")
					}
					if wantNew || historicalRead {
						require.Equal(t, 1, strings.Count(state.FinalAnswer, "60.6"), state.FinalAnswer)
						require.Equal(t, 1, strings.Count(state.FinalAnswer, "62.3"), state.FinalAnswer)
						require.Contains(t, state.FinalAnswer, "棉花周报20240310.pptx")
						require.Contains(t, state.FinalAnswer, "棉花周报20240310-冲突测试版.pptx")
						require.Regexp(t, `第\s*18\s*页`, state.FinalAnswer)
						require.NotContains(t, state.FinalAnswer, "本轮只检查了")
						require.NotContains(t, state.FinalAnswer, "本轮已读内容没有覆盖")
					} else {
						require.NotContains(t, state.FinalAnswer, "60.6")
						require.NotContains(t, state.FinalAnswer, "62.3")
					}
				})
			}
		})
	}
}
