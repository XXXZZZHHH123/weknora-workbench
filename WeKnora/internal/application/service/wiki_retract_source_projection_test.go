package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestWikiRetractProjectionProviderSchemaValidatesTheReplyContract(t *testing.T) {
	var document any
	require.NoError(t, json.Unmarshal(wikiRetractProjectionSchema, &document))
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://example.invalid/wiki-retract-projection.schema.json"
	require.NoError(t, compiler.AddResource(schemaURL, document))
	schema, err := compiler.Compile(schemaURL)
	require.NoError(t, err)
	decode := func(raw string) any {
		var value any
		require.NoError(t, json.Unmarshal([]byte(raw), &value))
		return value
	}
	require.NoError(t, schema.Validate(decode(`{"schema_version":1,"windows":[{"window_id":"w","source_id":"s","chunk_id":"c","quotes":[]}]}`)))
	for _, invalid := range []string{
		`{"schema_version":1,"windows":[]}`,
		`{"schema_version":1,"windows":[{"window_id":"w","source_id":"s","quotes":[]}]}`,
		`{"schema_version":1,"windows":[{"window_id":"w","source_id":"s","chunk_id":"c","quotes":[{"source_quote":"fabricated partial reply"}]}]}`,
		`{"schema_version":1,"windows":[{"window_id":"w","source_id":"s","chunk_id":"c","quotes":[],"extra":true}]}`,
	} {
		require.Error(t, schema.Validate(decode(invalid)))
	}
}

type wikiRetractProjectionChat struct {
	responses []*types.ChatResponse
	errors    []error
	calls     [][]chat.Message
	opts      []*chat.ChatOptions
	onCall    func()
}

func (m *wikiRetractProjectionChat) Chat(_ context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	m.calls = append(m.calls, append([]chat.Message(nil), messages...))
	m.opts = append(m.opts, opts)
	i := len(m.calls) - 1
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	if i >= len(m.responses) {
		return nil, fmt.Errorf("unexpected projection call %d", i+1)
	}
	response := m.responses[i]
	if m.onCall != nil {
		m.onCall()
	}
	return response, nil
}

func (m *wikiRetractProjectionChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	return nil, errors.New("streaming is not used by the wiki projection helper")
}

func (*wikiRetractProjectionChat) GetModelName() string { return "projection-test-model" }
func (*wikiRetractProjectionChat) GetModelID() string   { return "projection-test-id" }

func wikiRetractProjectionTestRequest(chunks []wikiRetractProjectionChunk) wikiRetractProjectionRequest {
	for i := range chunks {
		chunks[i].SourceID = "source-1"
	}
	return wikiRetractProjectionRequest{
		PageSlug: "concept/coastal-planning", PageTitle: "Coastal Planning",
		PageSummary:      "The previous page describes coastal resilience.",
		PageBody:         "Wetland buffers reduced flood risk for coastal communities.",
		Sources:          []wikiRetractProjectionSource{{ID: "source-1", Title: "Coastal Adaptation Report", Chunks: chunks}},
		ValidateSnapshot: func(context.Context, []wikiRetractChunkDigest) error { return nil },
	}
}

func wikiRetractProjectionBatchResponse(t *testing.T, windows []wikiRetractProjectionWindow, selections map[string][]wikiRetractWindowQuoteSelection) *types.ChatResponse {
	t.Helper()
	replies := make([]wikiRetractWindowReply, 0, len(windows))
	for _, window := range windows {
		quotes, ok := selections[window.ID]
		if !ok {
			quotes = []wikiRetractWindowQuoteSelection{}
		}
		replies = append(replies, wikiRetractWindowReply{WindowID: window.ID, SourceID: window.SourceID, ChunkID: window.ChunkID, Quotes: quotes})
	}
	encoded, err := json.Marshal(wikiRetractBatchReply{SchemaVersion: 1, Windows: replies})
	require.NoError(t, err)
	return &types.ChatResponse{Content: string(encoded), FinishReason: "stop", Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}
}

func wikiRetractProjectionResponse(t *testing.T, window wikiRetractProjectionWindow, quotes []wikiRetractWindowQuoteSelection) *types.ChatResponse {
	return wikiRetractProjectionBatchResponse(t, []wikiRetractProjectionWindow{window}, map[string][]wikiRetractWindowQuoteSelection{window.ID: quotes})
}

func wikiRetractProjectionSelection(window wikiRetractProjectionWindow, sourceQuote, pageQuote string) wikiRetractWindowQuoteSelection {
	return wikiRetractWindowQuoteSelection{
		SourceID: window.SourceID, ChunkID: window.ChunkID, WindowID: window.ID,
		SourceQuote: sourceQuote, PageField: "body", PageQuote: pageQuote, Relation: "supports_old_claim",
	}
}

func wikiRetractProjectionSelectionWithContexts(window wikiRetractProjectionWindow, sourceQuote string, sourceContext wikiRetractQuoteMatchContext, pageQuote string, pageContext wikiRetractQuoteMatchContext) wikiRetractWindowQuoteSelection {
	selection := wikiRetractProjectionSelection(window, sourceQuote, pageQuote)
	selection.SourceBefore, selection.SourceAfter = sourceContext.Before, sourceContext.After
	selection.PageBefore, selection.PageAfter = pageContext.Before, pageContext.After
	return selection
}

func decodeProjectionWindow(t *testing.T, messages []chat.Message) wikiRetractProjectionWindow {
	t.Helper()
	var request struct {
		Page struct {
			Slug    string `json:"slug"`
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Body    string `json:"body"`
		} `json:"previous_page"`
		Windows []wikiRetractProjectionWindow `json:"source_windows"`
	}
	require.Len(t, messages, 2)
	require.Equal(t, "system", messages[0].Role)
	require.Equal(t, "user", messages[1].Role)
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &request))
	require.NotEmpty(t, request.Windows)
	return request.Windows[0]
}

func TestWikiRetractProjectionScansLongSourceAndSelectsExactTailEvidence(t *testing.T) {
	tailQuote := "The restored marsh stores stormwater and lowers flood damage."
	content := strings.Repeat("Unrelated coastal planning material. ", 340) + tailQuote
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "chunk-tail", Index: 4, Content: content}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	require.Greater(t, len(windows), 1)
	require.Contains(t, windows[len(windows)-1].Content, tailQuote)
	pageQuote := "Wetland buffers reduced flood risk"
	request.PageBody = pageQuote + " for coastal communities."
	batches, err := batchWikiRetractProjectionWindows(windows)
	require.NoError(t, err)
	responses := make([]*types.ChatResponse, len(batches))
	for i, batch := range batches {
		selections := map[string][]wikiRetractWindowQuoteSelection{}
		for _, window := range batch {
			if strings.Contains(window.Content, tailQuote) {
				selections[window.ID] = []wikiRetractWindowQuoteSelection{wikiRetractProjectionSelection(window, tailQuote, pageQuote)}
			}
		}
		responses[i] = wikiRetractProjectionBatchResponse(t, batch, selections)
	}
	model := &wikiRetractProjectionChat{responses: responses}

	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.True(t, result.Scope.ScanComplete)
	require.Equal(t, len(windows), result.Scope.ReviewedWindows)
	require.Equal(t, len(windows), len(result.Receipts))
	require.Len(t, result.Quotes, 1)
	quote := result.Quotes[0]
	require.Equal(t, "source-1", quote.SourceID)
	require.Equal(t, "chunk-tail", quote.ChunkID)
	require.Equal(t, tailQuote, quote.SourceQuote)
	require.Equal(t, pageQuote, quote.PageQuote)
	byteStart := strings.Index(content, tailQuote)
	require.Equal(t, utf8.RuneCountInString(content[:byteStart]), quote.StartRune)
	require.Equal(t, quote.StartRune+utf8.RuneCountInString(tailQuote), quote.EndRune)
	require.Equal(t, len(batches)*10, result.Usage.PromptTokens)
	require.Equal(t, len(batches)*12, result.Usage.TotalTokens)
	for i, call := range model.calls {
		window := decodeProjectionWindow(t, call)
		require.LessOrEqual(t, utf8.RuneCountInString(window.Content), wikiRetractProjectionWindowRunes)
		var batchInput struct {
			Windows []wikiRetractProjectionWindow `json:"source_windows"`
		}
		require.NoError(t, json.Unmarshal([]byte(call[1].Content), &batchInput))
		require.LessOrEqual(t, len(batchInput.Windows), wikiRetractProjectionMaxWindowsPerCall)
		totalWindowRunes := 0
		for _, item := range batchInput.Windows {
			totalWindowRunes += utf8.RuneCountInString(item.Content)
		}
		require.LessOrEqual(t, totalWindowRunes, wikiRetractProjectionWindowRunes)
		require.Equal(t, request.PageBody, func() string {
			var envelope struct {
				Previous struct {
					Body string `json:"body"`
				} `json:"previous_page"`
			}
			require.NoError(t, json.Unmarshal([]byte(call[1].Content), &envelope))
			return envelope.Previous.Body
		}(), "every source window must be compared with the complete previous page")
		// No tools are provided to this JSON-only projection call. An empty
		// ToolChoice therefore leaves no callable tools available; production
		// also rejects any unexpected tool-call response.
		require.Empty(t, model.opts[i].ToolChoice)
		require.JSONEq(t, string(wikiRetractProjectionSchema), string(model.opts[i].Format))
	}
}

func TestWikiRetractProjectionOverlapFindsEvidenceAtWindowBoundary(t *testing.T) {
	quote := strings.Repeat("海", 260) + " restored the wetland buffer and reduced peak runoff by 18 percent."
	content := strings.Repeat("x", wikiRetractProjectionWindowRunes-80) + quote + strings.Repeat("z", 100)
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "boundary-chunk", Index: 0, Content: content}})
	request.PageBody = "The buffer reduced peak runoff."
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	require.Len(t, windows, 2)
	quoteStart := utf8.RuneCountInString(content[:strings.Index(content, quote)])
	require.GreaterOrEqual(t, quoteStart, wikiRetractProjectionWindowRunes-wikiRetractProjectionWindowOverlap)
	require.Less(t, quoteStart, wikiRetractProjectionWindowRunes)
	responses := []*types.ChatResponse{
		wikiRetractProjectionResponse(t, windows[0], []wikiRetractWindowQuoteSelection{}),
		wikiRetractProjectionResponse(t, windows[1], []wikiRetractWindowQuoteSelection{wikiRetractProjectionSelection(windows[1], quote, "reduced peak runoff")}),
	}
	result, err := projectWikiRetractSourceEvidence(context.Background(), &wikiRetractProjectionChat{responses: responses}, request)
	require.NoError(t, err)
	require.Len(t, result.Quotes, 1)
	require.Equal(t, quoteStart, result.Quotes[0].StartRune)
	require.Equal(t, quoteStart+utf8.RuneCountInString(quote), result.Quotes[0].EndRune)
}

func TestWikiRetractProjectionNoSelectedQuotesDoesNotClaimNoSourceSupport(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "quiet-chunk", Index: 0, Content: "General material with no selected quotation."}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, windows[0], []wikiRetractWindowQuoteSelection{})}}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.True(t, result.Scope.ScanComplete)
	require.Equal(t, "all_windows_reviewed_selected_none", result.Scope.Selection)
	require.Empty(t, result.Quotes)
	require.Contains(t, result.Scope.Notice, "does not prove")
}

func TestWikiRetractProjectionRejectsForgedCrossSourceAndAmbiguousQuotes(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "real-chunk", Index: 0, Content: "A real surviving source states one clear fact. Another fact is listed."}})
	window, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	tests := []struct {
		name  string
		quote wikiRetractWindowQuoteSelection
	}{
		{name: "invented source text", quote: wikiRetractProjectionSelection(window[0], "a plausible but fabricated quote", "Wetland buffers reduced flood risk")},
		{name: "cross source identifier", quote: func() wikiRetractWindowQuoteSelection {
			q := wikiRetractProjectionSelection(window[0], "one clear fact", "Wetland buffers reduced flood risk")
			q.SourceID = "deleted-source"
			return q
		}()},
		{name: "invented old page text", quote: wikiRetractProjectionSelection(window[0], "one clear fact", "A page claim that never existed")},
		{name: "ambiguous repeated quote", quote: wikiRetractProjectionSelection(window[0], "fact", "Wetland buffers reduced flood risk")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, window[0], []wikiRetractWindowQuoteSelection{tt.quote})}}
			_, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
			require.Error(t, err)
		})
	}
}

func TestWikiRetractProjectionUsesExactContextToDisambiguateRepeatedQuote(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "repeat-chunk", Index: 0, Content: "one repeated phrase; then two repeated phrase."}})
	request.PageBody = "The report contains repeated phrase."
	window, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	selection := wikiRetractProjectionSelection(window[0], "repeated phrase", "repeated phrase")
	selection.SourceBefore = "two "
	selection.PageBefore = "contains "
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, window[0], []wikiRetractWindowQuoteSelection{selection})}}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.Len(t, result.Quotes, 1)
	require.Equal(t, utf8.RuneCountInString("one repeated phrase; then two "), result.Quotes[0].StartRune)
	require.Equal(t, utf8.RuneCountInString("The report contains "), result.Quotes[0].PageStartRune)
}

func TestWikiRetractProjectionFailsClosedOnWindowAndQuoteBudgets(t *testing.T) {
	tooLong := strings.Repeat("长", wikiRetractProjectionMaxSourceRunes+1)
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "oversized", Index: 0, Content: tooLong}})
	model := &wikiRetractProjectionChat{}
	_, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Empty(t, model.calls, "the helper must detect an over-budget dossier before spending model calls")

	chunks := make([]wikiRetractProjectionChunk, 28)
	page := strings.Builder{}
	for i := range chunks {
		content := fmt.Sprintf("source-%02d|%s", i, strings.Repeat("q", wikiRetractProjectionMaxQuoteRunes-10))
		claim := fmt.Sprintf("claim-%02d", i)
		chunks[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("chunk-%02d", i), Index: i, SourceID: "source-1", Content: content}
		page.WriteString(claim + "; ")
	}
	request = wikiRetractProjectionTestRequest(chunks)
	request.PageBody = page.String()
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	require.Len(t, windows, len(chunks))
	batches, err := batchWikiRetractProjectionWindows(windows)
	require.NoError(t, err)
	responses := make([]*types.ChatResponse, len(batches))
	for i, batch := range batches {
		selections := map[string][]wikiRetractWindowQuoteSelection{}
		for _, window := range batch {
			chunkIndex := window.ChunkIndex
			claim := fmt.Sprintf("claim-%02d", chunkIndex)
			selections[window.ID] = []wikiRetractWindowQuoteSelection{wikiRetractProjectionSelection(window, request.Sources[0].Chunks[chunkIndex].Content, claim)}
		}
		responses[i] = wikiRetractProjectionBatchResponse(t, batch, selections)
	}
	responses = append(responses, responses[len(responses)-1], responses[len(responses)-1])
	model = &wikiRetractProjectionChat{responses: responses}
	_, err = projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "selected quotation budget")
	require.Len(t, model.calls, len(batches)+wikiRetractProjectionMaxRepairAttempts)
	require.LessOrEqual(t, len(model.calls), wikiRetractProjectionMaxCalls)
}

func TestWikiRetractProjectionRequiresSnapshotRevalidationAndPropagatesModelErrors(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "stable-chunk", Index: 0, Content: "Short verified source text."}})
	window, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	validationCalls := 0
	request.ValidateSnapshot = func(context.Context, []wikiRetractChunkDigest) error {
		validationCalls++
		if validationCalls == 2 {
			return errors.New("source changed while review was running")
		}
		return nil
	}
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, window[0], []wikiRetractWindowQuoteSelection{})}}
	_, err = projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "source snapshot changed")
	require.Equal(t, 2, validationCalls)

	request = wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "failed-chunk", Index: 0, Content: "Another verified source."}})
	model = &wikiRetractProjectionChat{errors: []error{errors.New("provider unavailable")}}
	_, err = projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "model failed for batch 1/1")
}

func TestLocateWikiRetractQuoteUsesRuneOffsetsAndRejectsContextGuessing(t *testing.T) {
	text := "海岸🌊计划：保留湿地。"
	start, end, err := locateUniqueWikiRetractQuote(text, "保留湿地", "：", "。")
	require.NoError(t, err)
	require.Equal(t, utf8.RuneCountInString("海岸🌊计划："), start)
	require.Equal(t, utf8.RuneCountInString("海岸🌊计划：保留湿地"), end)
	_, _, err = locateUniqueWikiRetractQuote("同一事实；同一事实", "同一事实", "", "")
	require.Error(t, err)
	_, _, err = locateUniqueWikiRetractQuote("唯一事实", "唯一事实", "错误前文", "")
	require.Error(t, err)
}

func TestWikiRetractProjectionRejectsNonFinalAndToolCallResponses(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "protocol-chunk", Index: 0, Content: "A source fact."}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	valid := wikiRetractProjectionResponse(t, windows[0], []wikiRetractWindowQuoteSelection{})
	for _, tc := range []struct {
		name     string
		response *types.ChatResponse
	}{
		{name: "provider completed alias is not a recognized natural stop", response: &types.ChatResponse{Content: valid.Content, FinishReason: "completed"}},
		{name: "tool call despite JSON response", response: func() *types.ChatResponse {
			r := *valid
			r.ToolCalls = []types.LLMToolCall{{ID: "unexpected"}}
			return &r
		}()},
		{name: "missing finish reason", response: &types.ChatResponse{Content: valid.Content}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectWikiRetractSourceEvidence(context.Background(), &wikiRetractProjectionChat{responses: []*types.ChatResponse{tc.response}}, request)
			require.Error(t, err)
		})
	}
}

func TestWikiRetractProjectionBatchesManySmallChunksAcrossSources(t *testing.T) {
	chunksA := make([]wikiRetractProjectionChunk, 50)
	chunksB := make([]wikiRetractProjectionChunk, 50)
	for i := 0; i < 50; i++ {
		chunksA[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("a-%03d", i), Index: i, SourceID: "source-a", Content: fmt.Sprintf("Coastal report section %d records routine planning context.", i)}
		chunksB[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("b-%03d", i), Index: i, SourceID: "source-b", Content: fmt.Sprintf("River basin section %d records routine planning context.", i)}
	}
	tailQuote := "The final wetland survey reduced flood losses by 18 percent."
	chunksB[len(chunksB)-1].Content += " " + tailQuote
	request := wikiRetractProjectionRequest{
		PageSlug: "concept/flood-planning", PageTitle: "Flood Planning", PageBody: "Wetland surveys reduced flood losses.",
		Sources: []wikiRetractProjectionSource{
			{ID: "source-b", Title: "River Basin Review", Chunks: chunksB},
			{ID: "source-a", Title: "Coastal Planning Review", Chunks: chunksA},
		}, ValidateSnapshot: func(context.Context, []wikiRetractChunkDigest) error { return nil },
	}
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	batches, err := batchWikiRetractProjectionWindows(windows)
	require.NoError(t, err)
	require.LessOrEqual(t, len(batches), wikiRetractProjectionMaxCalls)
	require.Less(t, len(batches), len(windows), "small source chunks should share bounded model calls")
	responses := make([]*types.ChatResponse, len(batches))
	for i, batch := range batches {
		selections := map[string][]wikiRetractWindowQuoteSelection{}
		for _, window := range batch {
			if strings.Contains(window.Content, tailQuote) {
				selections[window.ID] = []wikiRetractWindowQuoteSelection{wikiRetractProjectionSelection(window, tailQuote, "reduced flood losses")}
			}
		}
		responses[i] = wikiRetractProjectionBatchResponse(t, batch, selections)
	}
	model := &wikiRetractProjectionChat{responses: responses}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.True(t, result.Scope.ScanComplete)
	require.Len(t, result.Receipts, 100)
	require.Len(t, result.Quotes, 1)
	require.Equal(t, "source-b", result.Quotes[0].SourceID)
	require.Equal(t, "b-049", result.Quotes[0].ChunkID)
	for _, call := range model.calls {
		var envelope struct {
			Windows []wikiRetractProjectionWindow `json:"source_windows"`
		}
		require.NoError(t, json.Unmarshal([]byte(call[1].Content), &envelope))
		require.LessOrEqual(t, len(envelope.Windows), wikiRetractProjectionMaxWindowsPerCall)
		runes := 0
		sources := map[string]bool{}
		for _, window := range envelope.Windows {
			runes += utf8.RuneCountInString(window.Content)
			sources[window.SourceID] = true
		}
		require.LessOrEqual(t, runes, wikiRetractProjectionWindowRunes)
		require.LessOrEqual(t, len(sources), 2)
	}
}

func TestWikiRetractProjectionRejectsMissingOrMismatchedBatchWindow(t *testing.T) {
	chunks := make([]wikiRetractProjectionChunk, 3)
	for i := range chunks {
		chunks[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("small-%d", i), Index: i, Content: "A short source text."}
	}
	request := wikiRetractProjectionTestRequest(chunks)
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	require.Len(t, windows, 3)
	for _, tc := range []struct {
		name   string
		mutate func([]wikiRetractProjectionWindow) *types.ChatResponse
	}{
		{name: "missing window receipt", mutate: func(batch []wikiRetractProjectionWindow) *types.ChatResponse {
			return wikiRetractProjectionBatchResponse(t, batch[:2], nil)
		}},
		{name: "wrong window identity", mutate: func(batch []wikiRetractProjectionWindow) *types.ChatResponse {
			r := wikiRetractProjectionBatchResponse(t, batch, nil)
			var reply wikiRetractBatchReply
			require.NoError(t, json.Unmarshal([]byte(r.Content), &reply))
			reply.Windows[1].WindowID = "another-window"
			encoded, marshalErr := json.Marshal(reply)
			require.NoError(t, marshalErr)
			r.Content = string(encoded)
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{tc.mutate(windows)}}
			_, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
			require.Error(t, err)
		})
	}
}

func TestWikiRetractProjectionRejectsCancellationAfterProviderResponse(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "cancel-chunk", Index: 0, Content: "A source fact."}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := &wikiRetractProjectionChat{
		responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, windows[0], []wikiRetractWindowQuoteSelection{})},
		onCall:    cancel,
	}
	_, err = projectWikiRetractSourceEvidence(ctx, model, request)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWikiRetractProjectionRepairsRepeatedQuoteContextWithinSameBatch(t *testing.T) {
	const sourceQuote = "repeated source sentence"
	const pageQuote = "repeated page sentence"
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{
		ID: "chunk-repeat", Index: 0,
		Content: "Section Alpha: repeated source sentence. Section Beta: repeated source sentence.",
	}})
	request.PageBody = "Previous paragraph: repeated page sentence. Later paragraph: repeated page sentence."
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	window := windows[0]
	sourceContexts := wikiRetractQuoteMatchContexts(window.Content, sourceQuote)
	pageContexts := wikiRetractQuoteMatchContexts(request.PageBody, pageQuote)
	require.Len(t, sourceContexts, 2)
	require.Len(t, pageContexts, 2)

	withoutContext := wikiRetractProjectionSelection(window, sourceQuote, pageQuote)
	withSourceContext := wikiRetractProjectionSelection(window, sourceQuote, pageQuote)
	withSourceContext.SourceBefore, withSourceContext.SourceAfter = sourceContexts[0].Before, sourceContexts[0].After
	withBothContexts := wikiRetractProjectionSelectionWithContexts(window, sourceQuote, sourceContexts[0], pageQuote, pageContexts[1])
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{
		wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{withoutContext}),
		wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{withSourceContext}),
		wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{withBothContexts}),
	}}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.True(t, result.Scope.ScanComplete)
	require.Len(t, result.Quotes, 1)
	require.Equal(t, sourceQuote, result.Quotes[0].SourceQuote)
	require.Equal(t, pageQuote, result.Quotes[0].PageQuote)
	require.Len(t, model.calls, 3, "the first response plus at most two same-batch repairs are used")
	require.Equal(t, 36, result.Usage.TotalTokens, "usage from every actual repair call is retained")
	require.Len(t, model.calls[0], 2)
	require.Len(t, model.calls[1], 4, "repair keeps original messages and appends invalid assistant output plus feedback")
	require.Len(t, model.calls[2], 6, "second repair retains the first validation cycle too")
	require.Contains(t, model.calls[1][0].Content, wikiRetractProjectionRepairSystem)
	require.Equal(t, model.calls[0][1].Content, model.calls[1][1].Content)
	require.Equal(t, model.calls[0][1].Content, model.calls[2][1].Content)
	require.Equal(t, model.responses[0].Content, model.calls[1][2].Content)
	require.Equal(t, model.responses[1].Content, model.calls[2][4].Content)
	require.Contains(t, model.calls[1][2].Content, "schema_version")

	var sourceFeedback map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[1][3].Content), &sourceFeedback))
	require.LessOrEqual(t, len(model.calls[1][3].Content), wikiRetractProjectionRepairFeedbackBytes)
	require.Equal(t, "source_quote", sourceFeedback["field"])
	require.Equal(t, float64(0), sourceFeedback["quote_index"])
	sourceCandidates := sourceFeedback["candidate_contexts"].([]any)
	require.Len(t, sourceCandidates, 2)
	for _, raw := range sourceCandidates {
		candidate := raw.(map[string]any)
		require.Equal(t, "source", candidate["side"])
		require.LessOrEqual(t, utf8.RuneCountInString(candidate["before"].(string)), 64)
		require.LessOrEqual(t, utf8.RuneCountInString(candidate["after"].(string)), 64)
	}
	var pageFeedback map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[2][5].Content), &pageFeedback))
	require.LessOrEqual(t, len(model.calls[2][5].Content), wikiRetractProjectionRepairFeedbackBytes)
	require.Equal(t, "page_quote", pageFeedback["field"])
	require.Len(t, pageFeedback["candidate_contexts"].([]any), 2)
}

func TestWikiRetractProjectionRepairsIncorrectUniqueQuoteContext(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "chunk-context", Index: 0, Content: "Prefix exact source finding suffix."}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	window := windows[0]
	selection := wikiRetractProjectionSelection(window, "exact source finding", "Wetland buffers reduced flood risk")
	selection.SourceBefore = "incorrect"
	correctContext := wikiRetractQuoteMatchContexts(window.Content, selection.SourceQuote)[0]
	corrected := wikiRetractProjectionSelection(window, selection.SourceQuote, selection.PageQuote)
	corrected.SourceBefore, corrected.SourceAfter = correctContext.Before, correctContext.After
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{
		wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{selection}),
		wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{corrected}),
	}}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.Len(t, result.Quotes, 1)
	require.Len(t, model.calls, 2)
	var feedback struct {
		Field      string                         `json:"field"`
		Candidates []wikiRetractQuoteMatchContext `json:"candidate_contexts"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.calls[1][3].Content), &feedback))
	require.Equal(t, "source_quote", feedback.Field)
	require.NotEmpty(t, feedback.Candidates)
	require.Equal(t, correctContext, feedback.Candidates[0])
}

func TestWikiRetractProjectionRepairFeedbackCapsRealUnicodeContextsByJSONBytes(t *testing.T) {
	text := strings.Repeat(strings.Repeat("&", 64)+"NEEDLE"+strings.Repeat("<", 64)+"\n", 5)
	window := wikiRetractProjectionWindow{ID: "window-fingerprint-1234567890abcdef", Content: text}
	failure := wikiRetractValidationFailure("quote_location", window.ID, 2, "source_quote", "source quotation or adjacent context did not identify exactly one occurrence")
	failure.Side, failure.Quote = "source", "NEEDLE"
	feedback := wikiRetractProjectionRepairFeedback(failure, []wikiRetractProjectionWindow{window}, "", "", 1)
	require.LessOrEqual(t, len(feedback), wikiRetractProjectionRepairFeedbackBytes)
	var decoded struct {
		Contexts  []map[string]string `json:"candidate_contexts"`
		Truncated bool                `json:"candidate_contexts_truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(feedback), &decoded))
	require.Len(t, decoded.Contexts, 3)
	require.True(t, decoded.Truncated, "additional actual matches must be indicated when only the first three contexts fit the interface")
	for _, candidate := range decoded.Contexts {
		require.LessOrEqual(t, utf8.RuneCountInString(candidate["before"]), 64)
		require.LessOrEqual(t, utf8.RuneCountInString(candidate["after"]), 64)
		require.Contains(t, text, candidate["before"]+"NEEDLE"+candidate["after"])
	}
}

func TestWikiRetractProjectionFailedAttemptDoesNotCommitQuoteOrBudgetState(t *testing.T) {
	repeat := func(char rune, count int) string { return strings.Repeat(string(char), count) }
	chunks := make([]wikiRetractProjectionChunk, 15)
	pageParts := []string{}
	for i := 0; i < 13; i++ {
		quoteRunes := 1200
		if i == 12 {
			quoteRunes = 600
		}
		sourceQuote := repeat(rune('a'+i), quoteRunes)
		pageQuote := repeat(rune('A'+i), quoteRunes)
		content := sourceQuote + strings.Repeat("x", 5000-len(sourceQuote))
		chunks[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("chunk-prior-%02d", i), Index: i, Content: content}
		pageParts = append(pageParts, pageQuote)
	}
	q14aSource, q14aPage := repeat('N', 250), repeat('O', 250)
	q14bSource, q14bPage := repeat('n', 200), repeat('o', 200)
	q15aSource, q15aPage := repeat('P', 1000), repeat('Q', 1000)
	q15bSource, q15bPage := repeat('p', 250), repeat('q', 250)
	content14 := q14aSource + " divider " + q14bSource
	content15 := q15aSource + " divider " + q15bSource
	chunks[13] = wikiRetractProjectionChunk{ID: "chunk-failed-first-window", Index: 13, Content: content14 + strings.Repeat("x", 2000-len(content14))}
	chunks[14] = wikiRetractProjectionChunk{ID: "chunk-failed-second-window", Index: 14, Content: content15 + strings.Repeat("y", 2000-len(content15))}
	pageParts = append(pageParts, q14aPage, q14bPage, q15aPage, q15bPage)
	request := wikiRetractProjectionTestRequest(chunks)
	request.PageBody = strings.Join(pageParts, " | ")
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	batches, err := batchWikiRetractProjectionWindows(windows)
	require.NoError(t, err)
	require.Len(t, batches, 14)
	responses := make([]*types.ChatResponse, 0, len(batches)+1)
	for i := 0; i < 13; i++ {
		window := batches[i][0]
		quoteRunes := 1200
		if i == 12 {
			quoteRunes = 600
		}
		sourceQuote := repeat(rune('a'+i), quoteRunes)
		pageQuote := repeat(rune('A'+i), quoteRunes)
		selection := wikiRetractProjectionSelection(window, sourceQuote, pageQuote)
		responses = append(responses, wikiRetractProjectionResponse(t, window, []wikiRetractWindowQuoteSelection{selection}))
	}
	failedBatch := batches[13]
	require.Len(t, failedBatch, 2)
	initialSelections := map[string][]wikiRetractWindowQuoteSelection{
		failedBatch[0].ID: {wikiRetractProjectionSelection(failedBatch[0], q14aSource, q14aPage)},
		failedBatch[1].ID: {
			wikiRetractProjectionSelection(failedBatch[1], q15aSource, q15aPage),
			wikiRetractProjectionSelection(failedBatch[1], q15bSource, q15bPage),
		},
	}
	responses = append(responses, wikiRetractProjectionBatchResponse(t, failedBatch, initialSelections))
	repairSelections := map[string][]wikiRetractWindowQuoteSelection{
		failedBatch[0].ID: {wikiRetractProjectionSelection(failedBatch[0], q14bSource, q14bPage)},
		failedBatch[1].ID: {wikiRetractProjectionSelection(failedBatch[1], q15aSource, q15aPage)},
	}
	responses = append(responses, wikiRetractProjectionBatchResponse(t, failedBatch, repairSelections))
	model := &wikiRetractProjectionChat{responses: responses}
	result, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.NoError(t, err)
	require.Len(t, result.Quotes, 15, "the failed attempt's provisional quote identities and selected-rune count must not leak into the repair")
	require.Equal(t, q14bSource, result.Quotes[13].SourceQuote)
	require.Equal(t, q15aSource, result.Quotes[14].SourceQuote)
	repairCall := model.calls[len(model.calls)-1]
	require.Len(t, repairCall, 4)
	require.Equal(t, "system", repairCall[0].Role)
	require.Equal(t, model.calls[len(model.calls)-2][1], repairCall[1], "repair must preserve the exact original evidence request")
	require.Equal(t, "assistant", repairCall[2].Role)
	require.Equal(t, responses[len(responses)-2].Content, repairCall[2].Content)
	require.Equal(t, "user", repairCall[3].Role)
	require.NotEmpty(t, repairCall[3].Content)
	require.Equal(t, 180, result.Usage.TotalTokens)
}

func TestWikiRetractProjectionRepairsUseOnlyRemainingGlobalCallBudget(t *testing.T) {
	chunks := make([]wikiRetractProjectionChunk, 31)
	for i := range chunks {
		chunks[i] = wikiRetractProjectionChunk{ID: fmt.Sprintf("chunk-%02d", i), Index: i, Content: strings.Repeat("x", 5000)}
	}
	request := wikiRetractProjectionTestRequest(chunks)
	invalid := &types.ChatResponse{Content: "not JSON", FinishReason: "stop", Usage: types.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{invalid, invalid}}
	_, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Len(t, model.calls, 2, "31 initial batch slots leave only one repair call under the unchanged 32-call ceiling")
	require.LessOrEqual(t, len(model.calls), wikiRetractProjectionMaxCalls)
}

func TestWikiRetractProjectionStopsAfterTwoValidationRepairs(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "chunk-invalid", Index: 0, Content: "A source sentence."}})
	invalid := &types.ChatResponse{Content: "not JSON", FinishReason: "stop"}
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{invalid, invalid, invalid}}
	_, err := projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Len(t, model.calls, 1+wikiRetractProjectionMaxRepairAttempts)
	require.LessOrEqual(t, len(model.calls), wikiRetractProjectionMaxCalls)
}

func TestWikiRetractProjectionDoesNotRepairTransportLengthOrCancellation(t *testing.T) {
	request := wikiRetractProjectionTestRequest([]wikiRetractProjectionChunk{{ID: "chunk-stop", Index: 0, Content: "Exact source text."}})
	windows, _, err := buildWikiRetractProjectionWindows(request.Sources)
	require.NoError(t, err)
	length := wikiRetractProjectionResponse(t, windows[0], nil)
	length.FinishReason = "length"
	model := &wikiRetractProjectionChat{responses: []*types.ChatResponse{length}}
	_, err = projectWikiRetractSourceEvidence(context.Background(), model, request)
	require.Error(t, err)
	require.Len(t, model.calls, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceledModel := &wikiRetractProjectionChat{
		responses: []*types.ChatResponse{wikiRetractProjectionResponse(t, windows[0], nil)},
		onCall:    cancel,
	}
	_, err = projectWikiRetractSourceEvidence(ctx, canceledModel, request)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, canceledModel.calls, 1)
}
