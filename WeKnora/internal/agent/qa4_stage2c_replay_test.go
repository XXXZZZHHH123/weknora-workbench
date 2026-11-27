package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// The ordinals refer to the saved qa4-s2-2 SSE. Business arguments and drafts
// came from the same turns' persisted agent_steps. Directory entries and body
// chunks are excerpts, identifiers were replaced, and v2 hashes recalculated.
// Successful document reads were projected from the original stored source
// chunks because SSE intentionally omits their bodies and image transcription.
// KB-31's report read is a separate recovery variant using the stored slide.
// KB-16's actual latest response is from full-combo-all2 KB-16 SSE #24, with
// the same file hash, field and cell values under an earlier dataset alias.
type qa4Stage2CTrace struct {
	qa4Stage2Trace
	Documents       []*types.Knowledge
	Chunks          []*types.Chunk
	SearchHit       *types.SearchResult `json:"search_hit"`
	ScopedSearchHit *types.SearchResult `json:"scoped_search_hit"`
	LatestResponse  json.RawMessage     `json:"latest_response"`
}

// The native document tools perform the same authorization, binding and
// assembly as a live turn. The index and repositories are projected storage.
type qa4Stage2CDocuments struct {
	interfaces.KnowledgeService
	documents []*types.Knowledge
}

func (s *qa4Stage2CDocuments) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	for _, doc := range s.documents {
		if doc.ID == id {
			return doc, nil
		}
	}
	return nil, fmt.Errorf("no projected document for %q", id)
}

type qa4Stage2CChunks struct {
	interfaces.ChunkService
	repo *qa4Stage2CChunkRepository
}

func (s *qa4Stage2CChunks) GetRepository() interfaces.ChunkRepository { return s.repo }

func (s *qa4Stage2CChunks) GetChunkByIDOnly(_ context.Context, id string) (*types.Chunk, error) {
	for _, row := range s.repo.rows {
		if row.ID == id {
			s.repo.lookups = append(s.repo.lookups, id)
			return row, nil
		}
	}
	return nil, fmt.Errorf("no projected chunk for %q", id)
}

type qa4Stage2CChunkRepository struct {
	interfaces.ChunkRepository
	rows    []*types.Chunk
	lookups []string
}

func (s *qa4Stage2CChunkRepository) textRows(tenant uint64, document string) []*types.Chunk {
	var rows []*types.Chunk
	for _, row := range s.rows {
		if row.TenantID == tenant && row.KnowledgeID == document && row.ChunkType == types.ChunkTypeText {
			rows = append(rows, row)
		}
	}
	return rows
}

func (s *qa4Stage2CChunkRepository) ListPagedChunksByKnowledgeID(_ context.Context, tenant uint64, document string, page *types.Pagination, _ []types.ChunkType, _ []string, _, _, _, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	rows := s.textRows(tenant, document)
	start := min((page.Page-1)*page.PageSize, len(rows))
	end := min(start+page.PageSize, len(rows))
	return rows[start:end], int64(len(rows)), nil
}

func (s *qa4Stage2CChunkRepository) ListChunkNeighbors(_ context.Context, tenant uint64, document string, index, before, after int, _ []types.ChunkType) ([]*types.Chunk, error) {
	rows := s.textRows(tenant, document)
	at := slices.IndexFunc(rows, func(row *types.Chunk) bool { return row.ChunkIndex == index })
	if at < 0 {
		return nil, fmt.Errorf("no projected chunk at %d", index)
	}
	result := append([]*types.Chunk(nil), rows[max(0, at-before):at]...)
	return append(result, rows[at+1:min(at+after+1, len(rows))]...), nil
}

func (s *qa4Stage2CChunkRepository) ListChunksByParentIDs(_ context.Context, tenant uint64, parents []string) ([]*types.Chunk, error) {
	var rows []*types.Chunk
	for _, row := range s.rows {
		if row.TenantID == tenant && slices.Contains(parents, row.ParentChunkID) {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

type qa4Stage2CIndex struct {
	*sourceDiscoveryKnowledgeService
	result     *types.SearchResult
	additional []*types.SearchResult
	searches   []types.SearchParams
}

func (i *qa4Stage2CIndex) HybridSearch(_ context.Context, kb string, p types.SearchParams) ([]*types.SearchResult, error) {
	i.searches = append(i.searches, p)
	if i.result == nil || kb != qa4Stage2Library {
		return nil, nil
	}
	result := *i.result
	return append([]*types.SearchResult{&result}, i.additional...), nil
}

type qa4Stage2CModel struct {
	mockChat
	t           *testing.T
	reply       mockResponse
	review      mockResponse
	discovery   mockResponse
	reviewInput []chat.Message
	reviewCalls int
}

func (m *qa4Stage2CModel) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("unexpected non-streamed model call in historical replay")
}

func (m *qa4Stage2CModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	var next mockResponse
	switch purpose {
	case "agent_round":
		next, m.reply = m.reply, mockResponse{}
		if len(next.chunks) == 0 {
			return nil, fmt.Errorf("unexpected new execution round in historical replay")
		}
		if opts != nil && isReviewedAnswerFormat(opts.Format) && len(next.chunks[0].ToolCalls) == 0 {
			encoded, err := json.Marshal(map[string]string{"answer": next.chunks[0].Content})
			if err != nil {
				return nil, err
			}
			next = chatAnswer(string(encoded))
		}
	case "discovery_delivery":
		next = m.discovery
		if len(next.chunks) == 0 {
			next = discoveryReply("keep", "")
		}
	case "structured_result_delivery":
		m.reviewInput = messages
		m.reviewCalls++
		next = m.review
		if len(next.chunks) == 0 {
			next = lightReviewReply("keep", "")
		}
	default:
		m.t.Errorf("unexpected model purpose %q", purpose)
		return nil, fmt.Errorf("unexpected model purpose %q", purpose)
	}
	m.responses = append(m.responses, next)
	return m.mockChat.ChatStream(ctx, messages, opts)
}

type qa4Stage2CReplay struct {
	t           *testing.T
	trace       qa4Stage2CTrace
	ctx         context.Context
	engine      *AgentEngine
	state       *types.AgentState
	messages    []chat.Message
	model       *qa4Stage2CModel
	guards      loopGuards
	finals      []event.AgentFinalAnswerData
	results     map[int]types.ToolCall
	latestReads int
	index       *qa4Stage2CIndex
	repo        *qa4Stage2CChunkRepository
}

func newQA4Stage2CReplay(t *testing.T, fixture string, enabled, reportSearch bool, additionalSchemaCalls ...qa4Stage2Call) *qa4Stage2CReplay {
	t.Helper()
	const endpoint = "https://stage2c-structured.invalid"
	raw, err := os.ReadFile("testdata/qa4/replay/" + fixture + ".json")
	require.NoError(t, err)
	r := &qa4Stage2CReplay{t: t, state: &types.AgentState{}, results: map[int]types.ToolCall{}}
	require.NoError(t, json.Unmarshal(raw, &r.trace))
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", strconv.FormatBool(enabled))
	t.Setenv("STRUCTURED_DATA_URL", endpoint)
	key := t.TempDir() + "/signing-key"
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: qa4Stage2Library, TenantID: 1}}
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(targets))
	registry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	schemaCalls := slices.Concat(r.trace.Calls, additionalSchemaCalls)
	for _, raw := range manifest["tools"].([]any) {
		entry := raw.(map[string]any)
		props := entry["inputSchema"].(map[string]any)["properties"].(map[string]any)
		for _, call := range schemaCalls {
			if call.Tool == "structured_"+entry["name"].(string) {
				for k := range call.Arguments {
					if k != "knowledge_base_id" {
						props[k] = map[string]any{}
					}
				}
			}
		}
	}
	installed, err := registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, installed.Success)
	r.installTransport(endpoint)

	if len(r.trace.Documents) > 0 && (fixture != "s2-2-kb31" || reportSearch) {
		repo := &qa4Stage2CChunkRepository{rows: r.trace.Chunks}
		for _, row := range repo.rows {
			row.TenantID, row.KnowledgeBaseID, row.IsEnabled = 1, qa4Stage2Library, true
		}
		for _, doc := range r.trace.Documents {
			doc.TenantID, doc.KnowledgeBaseID, doc.ParseStatus = 1, qa4Stage2Library, types.ParseStatusCompleted
		}
		source := r.trace.Documents[len(r.trace.Documents)-1]
		page := repo.rows[0]
		for _, row := range repo.rows {
			if row.KnowledgeID == source.ID {
				page = row
				break
			}
		}
		r.index = &qa4Stage2CIndex{sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{}, result: &types.SearchResult{
			ID: page.ID, KnowledgeID: source.ID, KnowledgeBaseID: qa4Stage2Library, KnowledgeTitle: source.Title,
			Content: page.Content, ChunkIndex: page.ChunkIndex, SourceLocators: page.SourceLocators, Score: .99}}
		if r.trace.SearchHit != nil {
			r.index.result = r.trace.SearchHit
		}
		knowledge := &qa4Stage2CDocuments{documents: r.trace.Documents}
		chunks := &qa4Stage2CChunks{repo: repo}
		registry.RegisterTool(agenttools.NewSearchKnowledgeTool(r.index, knowledge, chunks, targets, nil, nil))
		registry.RegisterTool(agenttools.NewReadDocumentTool(knowledge, chunks, targets))
		agenttools.PrepareDocumentScope(registry, r.trace.Question)
		r.repo = repo
	}

	r.model = &qa4Stage2CModel{t: t}
	r.engine = newTestEngine(t, r.model, withCitationsEnabled(true), withMaxIterations(20))
	r.engine.toolRegistry = registry
	r.engine.config.WorkbenchDefault, r.engine.config.TaskPlanningEnabled, r.engine.config.StructuredResultReviewEnabled = true, true, true
	r.engine.config.AnswerReviewMode, r.engine.config.SearchTargets = "standard", targets
	r.engine.config.EvidenceReferencesEnabled = true
	r.engine.syncStructuredToolSourceContracts()
	r.ctx = withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "qa4-replay-reader", Role: types.TenantRoleViewer}))
	r.messages = []chat.Message{{Role: "system", Content: "Use authorized observations."}, {Role: "user", Content: r.trace.Question}}
	r.engine.reviewConversation, r.engine.reviewConversationReady = conversationThroughLastHuman(r.messages), true
	r.engine.turnReview.Query, r.engine.turnReview.Conversation = r.trace.Question, r.engine.reviewConversation
	r.engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
		r.finals = append(r.finals, item.Data.(event.AgentFinalAnswerData))
		return nil
	})
	return r
}

func (r *qa4Stage2CReplay) installTransport(endpoint string) {
	t := r.t
	original := http.DefaultTransport
	http.DefaultTransport = qa4Stage2Transport(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, endpoint+"/api/tools/read", request.URL.String())
		require.Equal(t, http.MethodPost, request.Method)
		var sent struct {
			Operation string
			Arguments map[string]any
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&sent))
		require.NotEmpty(t, request.Header.Get("X-Research-Signature"))
		var payload json.RawMessage
		for _, call := range r.trace.Calls {
			if call.Tool != "structured_"+sent.Operation {
				continue
			}
			expected := make(map[string]any, len(call.Arguments))
			for k, v := range call.Arguments {
				if k != "knowledge_base_id" {
					expected[k] = v
				}
			}
			if reviewDigest(expected) == reviewDigest(sent.Arguments) && r.results[call.Event].ID == "" {
				payload = call.Response
				break
			}
		}
		if len(payload) == 0 && sent.Operation == "query_series" && sent.Arguments["operation"] == "latest" && len(r.trace.LatestResponse) > 0 {
			original := r.results[21].ExecutionArgs()
			require.Equal(t, map[string]any{"dataset_id": original["dataset_id"], "series_id": original["series_id"], "operation": "latest", "limit": float64(1),
				"source_question": original["source_question"], "source_binding": original["source_binding"]}, sent.Arguments)
			r.latestReads++
			payload = r.trace.LatestResponse
		}
		if len(payload) == 0 {
			return nil, fmt.Errorf("no native tool receipt for %s: %v", sent.Operation, sent.Arguments)
		}
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func (r *qa4Stage2CReplay) setPlan(goal StructuredTaskGoal) {
	r.engine.taskPlan = &taskEvidencePlan{
		Goals:           []string{goal.Metric},
		SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured", "document"}},
		CoverageTargets: []taskCoverageTarget{{GoalIndex: 0, RequestBasis: r.trace.Question, StructuredTaskGoal: &goal}},
	}
}

func (r *qa4Stage2CReplay) planComparison() {
	// Reconstruct the truncated stored plan summary, not an additional
	// source declaration. History selected these exact two known documents.
	r.setPlan(StructuredTaskGoal{SourceMode: "open", VersionTask: "compare_sources", Metric: "工商业期末库存",
		Period:     map[string]any{"from": "2023-10-01", "to": "2023-10-31", "grain": "month"},
		SourceTime: "observed_period", Required: []string{"scoped_value"}})
}

func (r *qa4Stage2CReplay) withDeclaration(draft string, decl map[string]any) string {
	r.t.Helper()
	body, err := json.Marshal([]any{decl})
	require.NoError(r.t, err)
	return strings.TrimSpace(draft) + coverageAnswerTag + string(body) + coverageAnswerClose
}

func (r *qa4Stage2CReplay) run(response mockResponse) {
	r.t.Helper()
	require.False(r.t, r.state.IsComplete)
	r.model.reply = response
	_, err := r.engine.runReActIteration(r.ctx, r.state, &r.messages, r.engine.buildToolsForLLM(), "s2c", "turn", r.trace.Question, &r.guards)
	require.NoError(r.t, err)
	r.state.CurrentRound++
}

func (r *qa4Stage2CReplay) calls(calls []qa4Stage2Call) {
	r.t.Helper()
	var tools []types.LLMToolCall
	for _, call := range calls {
		args := make(map[string]any, len(call.Arguments))
		for k, v := range call.Arguments {
			if k == "knowledge_base_id" && strings.HasPrefix(call.Tool, "structured_") {
				v = r.engine.modelContext.RegisterKnowledgeBase(qa4Stage2Library)
			}
			args[k] = v
		}
		wire, err := json.Marshal(args)
		require.NoError(r.t, err)
		tools = append(tools, types.LLMToolCall{ID: call.id(), Type: "function", Function: types.FunctionCall{Name: call.Tool, Arguments: string(wire)}})
	}
	r.run(mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, ToolCalls: tools, Done: true, FinishReason: "tool_calls"}}})
	step := r.state.RoundSteps[len(r.state.RoundSteps)-1]
	for _, call := range calls {
		index := slices.IndexFunc(step.ToolCalls, func(got types.ToolCall) bool { return got.ID == call.id() })
		require.GreaterOrEqual(r.t, index, 0, "SSE #%d", call.Event)
		got := step.ToolCalls[index]
		require.NotNil(r.t, got.Result)
		r.results[call.Event] = got
		if strings.HasPrefix(call.Tool, "structured_") {
			require.True(r.t, got.Result.Success, "SSE #%d: %s", call.Event, got.Result.Error)
			if got.Result.Data["result_contract"] != nil {
				contract, err := structuredresult.ValidatePayload(got.Result.Data)
				require.NoError(r.t, err)
				require.NotNil(r.t, contract)
			}
		}
	}
}

func (r *qa4Stage2CReplay) through(last int) {
	r.t.Helper()
	var group []qa4Stage2Call
	for _, call := range r.trace.Calls {
		if call.Event > last {
			break
		}
		if _, done := r.results[call.Event]; done {
			continue
		}
		if len(group) > 0 && group[0].Iteration != call.Iteration {
			r.calls(group)
			group = nil
		}
		group = append(group, call)
	}
	if len(group) > 0 {
		r.calls(group)
	}
}

var qa4Stage2CEvidenceTag = regexp.MustCompile(`<evidence evidence_id="ev_[0-9a-f]{64}" />`)

// Public citation IDs include a runtime nonce. Reissue the same type and row
// selection using the native replay result; preserve the rest of the draft.
func (r *qa4Stage2CReplay) draft(index, eventNumber int) string {
	draft := r.trace.Drafts[index]
	if eventNumber == 26 {
		page := r.trace.Chunks[0]
		handle := r.engine.modelContext.CitableChunkHandle(page.ID)
		require.NotEmpty(r.t, handle, "historical comparison citation must refer to the actual page")
		tag := fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, r.trace.Documents[1].Title, page.ID, qa4Stage2Library)
		require.Contains(r.t, draft, tag)
		draft = strings.Replace(draft, tag, fmt.Sprintf(`<ref id="%s"/>`, handle), 1)
	}
	n := 0
	return qa4Stage2CEvidenceTag.ReplaceAllStringFunc(draft, func(tag string) string {
		row := n
		n++
		if eventNumber == 15 {
			row %= 3
		}
		for id, obs := range r.engine.turnReview.StructuredCitations {
			selector, _ := obs.Directory["selector"].(map[string]any)
			if obs.CallID == r.results[eventNumber].ID && selector["column_name"] == "value" &&
				selector["row_index"] != nil && citationInt(selector["row_index"]) == row {
				handle := r.engine.modelContext.CitableEvidenceHandle(id)
				require.NotEmpty(r.t, handle)
				return fmt.Sprintf(`<ref id="%s"/>`, handle)
			}
		}
		r.t.Errorf("no citation for replay event %d row %d", eventNumber, row)
		return tag
	})
}

func (r *qa4Stage2CReplay) answer() string {
	r.t.Helper()
	require.True(r.t, r.state.IsComplete, "%s %v", r.engine.turnReview.Last.ReasonCode, r.engine.turnReview.Last.Unsupported)
	require.NotEmpty(r.t, r.state.FinalAnswer)
	require.Len(r.t, r.finals, 2)
	require.Equal(r.t, r.state.FinalAnswer, r.finals[0].Content)
	require.False(r.t, r.finals[0].Done)
	require.True(r.t, r.finals[1].Done)
	for _, forbidden := range []string{"<task_coverage", "<source_coverage", "selection_ref", "数字待核对", "暂不提供", "请稍后重试", "所列原格来自本轮实读"} {
		require.NotContains(r.t, r.state.FinalAnswer, forbidden)
	}
	return r.state.FinalAnswer
}

func (r *qa4Stage2CReplay) exhaust() {
	r.t.Helper()
	if r.state.IsComplete {
		return
	}
	// The captured provider rounds ended here. Exercise production budget
	// settlement with the evidence already replayed, using no extra model.
	r.engine.stopEvidenceCompletion(r.ctx, r.trace.Question, r.state, r.messages, "replay_budget")
	r.engine.commitTerminalResult(r.ctx, r.state, "s2c", incompleteAnswerFallback, "", "partial", false)
}

func TestQA4Stage2CHistoricalReplay(t *testing.T) {
	t.Run("KB07_SQL_and_unit", func(t *testing.T) {
		for _, variant := range []struct {
			name          string
			requestedUnit bool
			withoutDraft  bool
		}{
			{"bounded_reviewer_correction", false, false},
			{"typed_unit_with_reviewer_keep", true, false},
			{"typed_unit_fallback_without_candidate", true, true},
		} {
			t.Run(variant.name, func(t *testing.T) {
				r := newQA4Stage2CReplay(t, "s2-2-kb07", true, false)
				r.through(15)
				steps := r.state.RoundSteps
				claim := "第87行；另一个观测值为87万吨。"
				require.Equal(t, []string{"87"}, ungroundedStructuredNumbers(claim, r.engine.answerNumericEvidence(claim, steps), steps))
				require.Empty(t, ungroundedStructuredNumbers("第87行", r.engine.answerNumericEvidence("", steps), steps))
				draft := r.draft(0, 15)
				require.Contains(t, draft, "不能直接当成万吨")
				if variant.requestedUnit {
					// This is the new execution declaration, not historical model output.
					definition := map[string]any{"goal_index": 0, "request_basis": r.trace.Question,
						"source_mode": "named_first", "primary_ref": "棉花数据库", "metric": "中国棉花平衡表",
						"requested_unit": "万吨", "period": map[string]any{"grain": "day", "from": "2023-10-31", "to": "2023-10-31"},
						"source_time": "observed_period", "delivery": map[string]any{"kind": "text", "aggregation": "as_reported"}}
					selections := []any{map[string]any{"goal_index": 0, "goal": definition}}
					for ref, observation := range r.engine.evidenceCompletion.refs {
						if observation.Operation == "analyze_table" && observation.Kind == "cell" {
							selections = append(selections, map[string]any{"goal_index": 0, "role": "primary", "selection_ref": ref,
								"period_from": observation.PeriodFrom, "period_to": observation.PeriodTo, "grain": observation.Grain})
						}
					}
					require.Len(t, selections, 4)
					declarations, err := json.Marshal(selections)
					require.NoError(t, err)
					if !variant.withoutDraft {
						// This is a complete model response. Check that
						// publication keeps its requested-unit explanation.
						for _, pair := range [][2]string{{"231.93 ", "231.93 万吨 "}, {"110.00 ", "110.00 万吨 "}, {"298.93 ", "298.93 万吨 "}} {
							draft = strings.Replace(draft, pair[0], pair[1], 1)
						}
						paragraphs := strings.Split(strings.TrimSpace(draft), "\n\n")
						paragraphs[len(paragraphs)-1] = "这三列原表未标单位，按问题所给单位列示，原值未换算。"
						draft = strings.Join(paragraphs, "\n\n")
					}
					draft = strings.TrimSpace(draft) + coverageAnswerTag + string(declarations) + coverageAnswerClose
					r.model.review = lightReviewReply("keep", "")
				} else {
					// The historical source reviewer returned "keep". The
					// current bounded reviewer receives the same input plus
					// the new instruction. Replay a compliant minimal edit.
					disclaimer := strings.Split(strings.TrimSpace(draft), "\n\n")
					r.model.review = lightReviewEdits(
						structuredResultEdit{"工商业期初库存：231.93", "工商业期初库存：231.93 万吨"},
						structuredResultEdit{"产量：110.00", "产量：110.00 万吨"},
						structuredResultEdit{"工商业期末库存：298.93", "工商业期末库存：298.93 万吨"},
						structuredResultEdit{disclaimer[len(disclaimer)-1], "这三列原表未标单位，按问题所给单位列示，原值未换算。"})
				}
				if variant.withoutDraft {
					// Apply the declared selection through the usual boundary,
					// then simulate settlement without any candidate to publish.
					r.engine.acceptCoverageAnswer(r.ctx, r.trace.Question, r.state, r.messages, draft, true)
					require.Empty(t, r.engine.turnReview.CoverageDraft.Candidate)
					require.Empty(t, r.engine.turnReview.Last.Candidate)
					r.exhaust()
				} else {
					r.run(chatAnswer(draft))
					require.True(t, r.state.IsComplete, "grounded draft must settle normally: %s", r.engine.turnReview.Last.ReasonCode)
				}
				answer := r.answer()
				for _, want := range []string{"231.93", "110", "298.93", "B87", "C87", "G87", "2023-10-31"} {
					require.Contains(t, answer, want)
				}
				require.NotContains(t, answer, "不能直接当成万吨")
				if variant.withoutDraft {
					require.Contains(t, answer, "单位未确认")
					require.NotContains(t, answer, "按问题所给单位")
					require.Empty(t, qa4Stage2CEvidenceTag.FindAllString(answer, -1))
					require.Zero(t, r.model.reviewCalls)
					return
				}
				require.Regexp(t, `231\.93[0-9]*\s*万吨`, answer)
				require.Regexp(t, `110(?:\.0+)?\s*万吨`, answer)
				require.Regexp(t, `298\.93[0-9]*\s*万吨`, answer)
				require.Contains(t, answer, "按问题所给单位")
				if variant.requestedUnit {
					require.Equal(t, r.engine.modelContext.DecodeOutputText(stripCoverageAnswer(draft)), answer)
				}
				require.NotContains(t, answer, "取值格 ", "the retained candidate already delivers the referenced values and coordinates")
				require.Len(t, qa4Stage2CEvidenceTag.FindAllString(answer, -1), 6)
				var completed event.AgentCompleteData
				r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
					completed = item.Data.(event.AgentCompleteData)
					return nil
				})
				r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
				require.Equal(t, answer, r.state.FinalAnswer)
				require.Equal(t, answer, completed.FinalAnswer)
				require.Len(t, r.state.EvidenceRefs, 3, "the six inline citations resolve to three stored observations")
				require.Len(t, completed.EvidenceRefs, 3)
				var durableIDs []string
				for _, ref := range r.state.EvidenceRefs {
					durableIDs = append(durableIDs, ref.ID)
				}
				require.ElementsMatch(t, uniqueCandidateEvidenceIDs(answer), durableIDs)
				require.Equal(t, 1, r.model.reviewCalls)
				if !variant.requestedUnit {
					require.NotEmpty(t, r.model.reviewInput)
					require.Contains(t, r.model.reviewInput[0].Content, structuredResultRequestedUnitInstructions)
				}
			})
		}
	})
	t.Run("KB07_rollback_retains_legacy_row_check", func(t *testing.T) {
		r := newQA4Stage2CReplay(t, "s2-2-kb07", false, false)
		r.through(15)
		steps := r.state.RoundSteps
		const claim = "第87行"
		require.Equal(t, []string{"87"}, ungroundedStructuredNumbers(claim, r.engine.answerNumericEvidence(claim, steps), steps))
	})
	t.Run("KB16_malformed_declaration_and_authorized_latest", func(t *testing.T) {
		for _, enabled := range []bool{false, true} {
			t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
				r := newQA4Stage2CReplay(t, "s2-2-kb16", enabled, false)
				r.through(31)
				for _, draft := range r.trace.Drafts {
					require.NotContains(t, draft, "17110", "no historical model response supplies the last price")
					require.Contains(t, draft, "<task_coverage>{")
					if !r.state.IsComplete {
						r.run(chatAnswer(draft))
					}
				}
				answer := r.answer()
				for _, want := range []string{"2024-03-01", "2024-03-31", "棉花数据库.xlsx", "郑棉期货", "无有效记录"} {
					require.Contains(t, answer, want)
				}
				require.NotContains(t, answer, "2023-09-21", "the spread field's original endpoint was never read")
				if !enabled {
					require.Zero(t, r.latestReads)
					require.NotContains(t, answer, "2023-09-22", "the published date has no original value read")
					require.NotContains(t, answer, "17110")
					require.NotContains(t, answer, "B4705")
					return
				}
				require.Equal(t, 1, r.latestReads)
				for _, want := range []string{"2023-09-22", "17110", "A4705", "B4705"} {
					require.Contains(t, answer, want)
				}
			})
		}
	})
	t.Run("KB28_grounded_comparison", func(t *testing.T) {
		for _, historicalSearch := range []bool{true, false} {
			t.Run(strconv.FormatBool(historicalSearch), func(t *testing.T) {
				r := newQA4Stage2CReplay(t, "s2-2-kb28", true, false)
				if !historicalSearch {
					// Drop the other specific hit, where a different table
					// happened to contain the same number as the draft's delta.
					r.index.result = r.trace.ScopedSearchHit
				}
				r.planComparison()
				r.through(30)
				require.False(t, r.results[8].Result.Success)
				require.Contains(t, r.results[8].Result.Error, "来源计划")
				require.True(t, r.results[14].Result.Success)
				bound, err := json.Marshal(r.results[14].Result.Data["documents"])
				require.NoError(t, err)
				require.Contains(t, string(bound), "棉花数据库.xlsx")
				require.Contains(t, string(bound), "棉花周报20240331.pptx")
				require.True(t, r.results[30].Result.Success, r.results[30].Result.Error)
				require.Contains(t, r.results[30].Result.Output, "331.2")
				require.NotContains(t, r.repo.rows[0].Content, "331.2")
				saved := r.draft(0, 26)
				require.Contains(t, saved, "32.3", "the archived model added an unverified optional calculation")
				// A separate model response retains the sourced comparison.
				// The product cannot edit the archived draft on publication.
				revised, _, ok := strings.Cut(saved, "\n\n**差异说明**")
				require.True(t, ok)
				r.engine.evidenceCompletion.enabled = false
				r.run(chatAnswer(revised))
				r.exhaust()
				answer := r.answer()
				require.Equal(t, r.engine.modelContext.DecodeOutputText(revised), answer)
				for _, want := range []string{"不一致", "331.2", "298.93", "G87", "2024年3月31日周报", "第6页", "<kb ", "<evidence "} {
					require.Contains(t, answer, want)
				}
				require.NotContains(t, answer, "32.3", "the independently unverified optional paragraph is omitted")
				require.NotContains(t, answer, "部分来源请求在读取开始前被拦下")
			})
		}
	})
	t.Run("KB28_chunk_calls_under_recovery", func(t *testing.T) {
		r := newQA4Stage2CReplay(t, "s2-2-kb28", true, false)
		r.planComparison()
		r.through(30)
		r.engine.beginEvidenceCompletionRecovery(r.ctx, r.state)
		require.NotNil(t, r.engine.evidenceCompletion.pending)
		r.through(51)
		for _, eventNumber := range []int{36, 40, 43, 48, 51} {
			result := r.results[eventNumber].Result
			require.NotNil(t, result)
			require.False(t, result.PreflightRejected, "SSE #%d: %s", eventNumber, result.Error)
			require.True(t, result.Success, "SSE #%d: %s", eventNumber, result.Error)
			require.True(t, agenttools.IsCurrentDocumentRead(r.engine.toolRegistry, result), "SSE #%d: receipt belongs to the currently bound source", eventNumber)
		}
		require.Contains(t, r.repo.lookups, "replay-comparison-page")
		require.Contains(t, r.repo.lookups, "replay-comparison-page-next")
	})
	t.Run("KB31_actual_and_authorized_report_variant", func(t *testing.T) {
		for _, searchReport := range []bool{false, true} {
			t.Run(strconv.FormatBool(searchReport), func(t *testing.T) {
				r := newQA4Stage2CReplay(t, "s2-2-kb31", true, searchReport)
				r.setPlan(StructuredTaskGoal{SourceMode: "named_first", PrimaryRef: "棉花数据库", Metric: "郑棉仓单",
					Period: map[string]any{"from": "2024-03-10", "to": "2024-03-10", "grain": "day"}, SourceTime: "observed_period",
					Required: []string{"scoped_value"}, OnVerifiedGap: []string{"report_gap", "last_valid_same_field", "attributed_supplement"}})
				r.through(25)
				for _, draft := range r.trace.Drafts {
					require.NotContains(t, draft, "60.6")
					if !r.state.IsComplete {
						r.run(chatAnswer(draft))
					}
					if searchReport && len(r.index.searches) > 0 {
						break
					}
				}
				if searchReport {
					require.Len(t, r.index.searches, 1, "the current runtime used the existing bounded search once")
					require.Equal(t, "郑棉仓单 2024-03-10", r.index.searches[0].QueryText)
					require.False(t, r.state.IsComplete)
					// The historical run made no document call. After the
					// directed search, replay the existing executor selecting
					// the real report and reading its page from authorized storage.
					doc := r.trace.Documents[0]
					read := qa4Stage2Call{Event: 101, Tool: agenttools.ToolReadDocument, Arguments: map[string]any{
						"id": doc.ID, "page": 18,
						"source_scope": map[string]any{"selection_reason": "核对授权补充资料的仓单披露及其口径", "sources": []any{map[string]any{
							"knowledge_id": doc.ID, "expected_file_name": doc.FileName, "page": 18,
							"date_evidence": map[string]string{"date": "2024-03-10", "source_field": "file_name", "source_text": "20240310"},
						}}},
					}}
					r.calls([]qa4Stage2Call{read})
					receipt := r.results[101].Result
					require.True(t, receipt.Success, receipt.Error)
					require.True(t, agenttools.IsCurrentDocumentRead(r.engine.toolRegistry, receipt))
					require.Contains(t, receipt.Output, "郑棉仓单：合计60.6万吨")
					require.Equal(t, 18, citationInt(receipt.Data["page"]))
					if !r.state.IsComplete {
						// Model rewrite for the variant, after the native read.
						// Cite the actual page while separating its printed total
						// from the original database observation.
						draft := "**棉花数据库.xlsx 的郑棉仓单表在2024-03-10没有有效值。**\n\n" +
							"仓单数量:一号棉花 最近有效值是5160，对应2023-09-21，日期格A3340，取值格B3340，原表未标单位。\n\n" +
							"授权补充来源《棉花周报20240310.pptx》（第18页）披露“郑棉仓单：合计60.6万吨”；这是周报披露口径，不能作为数据库该日仓单数量原值。" +
							fmt.Sprintf(`<ref id="%s"/>`, r.engine.modelContext.CitableChunkHandle(r.trace.Chunks[0].ID))
						selection := ""
						for ref, obs := range r.engine.evidenceCompletion.refs {
							if obs.ChunkID == r.trace.Chunks[0].ID && obs.Kind == "body" {
								selection = ref
								break
							}
						}
						require.NotEmpty(t, selection, "the supplement selects an actual body receipt")
						draft = r.withDeclaration(draft, map[string]any{"goal_index": 0, "selection_ref": selection, "role": "supplement",
							"page": 18, "target_field": "warehouse.B", "metric": "郑棉仓单", "unit": "万吨", "value_text": "60.6",
							"span": "郑棉仓单：合计60.6万吨"})
						r.engine.evidenceCompletion.enabled = false
						r.run(chatAnswer(draft))
						require.Equal(t, r.engine.modelContext.DecodeOutputText(stripCoverageAnswer(draft)), r.state.FinalAnswer)
					}
				}
				r.exhaust()
				answer := r.answer()
				for _, want := range []string{"2024-03-10", "2023-09-21", "5160", "B3340", "棉花数据库.xlsx"} {
					require.Contains(t, answer, want)
				}
				for _, unrelated := range []string{"A3338", "B3338", "C3338", "D3338", "E3338", "5467", "341", "5160 张", "S0049502"} {
					require.NotContains(t, answer, unrelated)
				}
				if searchReport {
					require.Contains(t, answer, "60.6万吨")
					require.NotContains(t, answer, "AF3340", "the related SQL input is not an additional requested value")
					require.Contains(t, answer, "棉花周报20240310.pptx")
					require.Contains(t, answer, "第18页")
					require.Contains(t, answer, "披露口径")
				} else {
					require.NotContains(t, answer, "60.6")
				}
			})
		}
	})
}
