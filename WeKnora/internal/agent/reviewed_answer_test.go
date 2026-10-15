package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReviewedAnswerUsesExplicitResponseContractWithoutLeakingEnvelope(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{Content: `{"answer":"原报告记载30。"}`, Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), nil, 0, "s")
	require.NoError(t, err)
	require.Equal(t, "原报告记载30。", response.Content)
	require.False(t, response.AnswerStreamed)
	require.JSONEq(t, string(reviewedAnswerSchema), string(model.opts[0].Format))
	require.Zero(t, model.opts[0].Temperature)
	require.Equal(t, "legacy text", decodeReviewedAnswer("legacy text"))
	require.Empty(t, decodeReviewedAnswer(`{"other":"not an answer"}`), "invalid JSON delivery contracts must not leak as raw answers")
	require.Empty(t, decodeReviewedAnswer(`{"answer":""}`), "empty delivery still goes through completeness repair")
}

func TestReviewedAnswerLosslesslyNormalizesLiteralStringControls(t *testing.T) {
	content := "First line\nSecond line\r\n\t<ref id=\"c1\"/> \\ original path"
	encoded, err := json.Marshal(map[string]string{"answer": content})
	require.NoError(t, err)
	valid := string(encoded)
	malformed := strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(valid)
	require.False(t, json.Valid([]byte(malformed)))
	require.Equal(t, content, decodeReviewedAnswer(valid))
	require.Equal(t, content, decodeReviewedAnswer(malformed))
	var normalized map[string]string
	require.NoError(t, json.Unmarshal([]byte(escapeJSONLiteralControls(malformed)), &normalized))
	require.Equal(t, content, normalized["answer"], "normalization changes string encoding only, never answer characters")
}

func TestReviewedAnswerRejectsIncompleteAndMultipleJSONWithoutInventingContent(t *testing.T) {
	for _, content := range []string{
		`{"answer":"unfinished`,
		"{\"answer\":\"unfinished\n",
		`{"answer":"complete"`,
		`{"answer":"one"} {"answer":"two"}`,
		`{"answer":"one","answer":"two"}`,
		`{"answer":"one","private_reasoning":"hidden"}`,
		`{"answer":null}`,
		`{"answer":7}`,
		`[{"answer":"wrong envelope"}]`,
		"{\"answer\":\"bad escape\\\ntext\"}",
		"{\"answer\":\"unescaped \" quote\"}",
	} {
		require.Empty(t, decodeReviewedAnswer(content), content)
	}
	require.Equal(t, "Ordinary provider prose.", decodeReviewedAnswer("Ordinary provider prose."))
}

func TestReviewedAnswerDecodesJSONBeforeSourceAndResourceHandles(t *testing.T) {
	const image = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const citation = `<kb doc="Report &#34;quoted&#34;" chunk_id="actual-chunk" kb_id="actual-kb" />`
	for _, payload := range []string{
		`{"answer":"结果30。<ref id=\"c1\"/> ![图](res://0001) <ref id=\"c2\"/> <ref id=\"c999\"/> [unknown](res://9999) <kb doc=\"forged\" chunk_id=\"forged\"/>"}`,
		`{"answer":"结果30。\u003cref id=\u0022c1\u0022/\u003e ![图](res://0001) \u003cref id=\u0022c2\u0022/\u003e \u003cref id=\u0022c999\u0022/\u003e [unknown](res://9999)"}`,
		// A legacy provider that ignores JSON mode retains the same citation
		// policy and later completeness/source checks as its plain-text path.
		`结果30。<ref id="c1"/> ![图](res://0001) <ref id="c2"/> <ref id="c999"/> [unknown](res://9999)`,
	} {
		for split := 1; split < len(payload); split++ {
			t.Run(fmt.Sprintf("%d/%d", len(payload), split), func(t *testing.T) {
				model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
					{ResponseType: types.ResponseTypeAnswer, Content: payload[:split]},
					{ResponseType: types.ResponseTypeAnswer, Content: payload[split:], Done: true, FinishReason: "stop"},
				}}}}
				engine := newTestEngine(t, model)
				engine.structuredReviewMode = true
				engine.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-chunk", KnowledgeBaseID: "actual-kb", DocumentTitle: `Report "quoted"`})
				engine.modelContext.RegisterContextChunk(modelcontext.ChunkReference{ChunkID: "navigation-only", KnowledgeBaseID: "actual-kb"})
				var emitted strings.Builder
				engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, value event.Event) error {
					if data, ok := value.Data.(event.AgentFinalAnswerData); ok {
						emitted.WriteString(data.Content)
					}
					return nil
				})
				response, err := engine.streamThinkingToEventBus(context.Background(), []chat.Message{{Role: "tool", Content: "actual image=" + image}}, nil, 0, "s")
				require.NoError(t, err)
				require.Contains(t, response.Content, citation)
				require.Contains(t, response.Content, "![图]("+image+")")
				require.NotContains(t, response.Content, "<ref")
				require.NotContains(t, response.Content, "navigation-only")
				require.NotContains(t, response.Content, "forged")
				require.NotContains(t, response.Content, "res://")
				require.NotContains(t, response.Content, `{"answer"`)
				require.Empty(t, emitted.String(), "the JSON envelope and unverified candidate must never stream to the user")
			})
		}
	}
}

func TestReviewedAnswerToolRequestsRetainArgumentDecodingAndNoDraftDelivery(t *testing.T) {
	for _, test := range []struct {
		input      string
		unresolved bool
	}{{`{"id":"d1"}`, false}, {`{"id":"d999"}`, true}} {
		model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
			ResponseType: types.ResponseTypeAnswer, Content: `{"answer":"继续读取<ref id=\"c1\"/>"}`,
			ToolCalls: []types.LLMToolCall{{ID: "read", Function: types.FunctionCall{Name: "read_document", Arguments: test.input}}},
			Done:      true, FinishReason: "tool_calls",
		}}}}}
		engine := newTestEngine(t, model)
		engine.structuredReviewMode = true
		engine.modelContext.RegisterDocument("actual-document")
		response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), nil, 0, "s")
		require.NoError(t, err)
		require.Empty(t, response.Content, "a review draft preceding requested tools is not a delivered answer")
		require.Len(t, response.ToolCalls, 1)
		if test.unresolved {
			require.NotEmpty(t, response.ToolCalls[0].UnresolvedHandles, "unknown handles still fail before execution")
		} else {
			require.JSONEq(t, `{"id":"actual-document"}`, response.ToolCalls[0].Function.Arguments)
			require.Empty(t, response.ToolCalls[0].UnresolvedHandles)
		}
		require.False(t, response.AnswerStreamed)
	}
}
