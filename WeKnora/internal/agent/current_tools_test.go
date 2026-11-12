package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/api/openaicompletions"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func currentToolNoticeNames(t *testing.T, notice string) []string {
	t.Helper()
	index := strings.LastIndex(notice, "<runtime_event>")
	require.NotEqual(t, -1, index)
	block := strings.SplitN(notice[index:], "</runtime_event>", 2)[0]
	var value struct {
		Type      string   `json:"type"`
		Scope     string   `json:"scope"`
		ToolNames []string `json:"available_tool_names"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(block, "<runtime_event>")), &value))
	require.Equal(t, "current_tool_availability", value.Type)
	require.Equal(t, "current_model_request", value.Scope)
	return value.ToolNames
}

func TestCurrentToolsFollowRuntimeAfterCapabilityChange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history string
		tools   []string
	}{
		{"disabled_then_enabled", "I cannot retrieve online sources in this turn.", []string{"web_search", "web_fetch"}},
		{"enabled_then_disabled", "I can search and read online sources.", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
				Content: "Current answer", Done: true, FinishReason: "stop",
			}}}}}
			engine := newTestEngine(t, model)
			// Configuration and old prose are deliberately stale. The actual
			// registered/projected definitions must remain authoritative.
			engine.config.WebSearchEnabled = len(tc.tools) == 0
			engine.toolRegistry = agenttools.NewToolRegistry()
			for _, name := range tc.tools {
				engine.toolRegistry.RegisterTool(newCountingTool(name))
			}
			history := []chat.Message{
				{Role: "user", Content: "Keep any confidential data private."},
				{Role: "assistant", Content: tc.history},
			}
			before := append([]chat.Message(nil), history...)
			state, err := engine.Execute(t.Context(), "session", "message", "Continue using the current capabilities.", history, nil)
			require.NoError(t, err)
			require.True(t, state.IsComplete)
			require.Len(t, model.calls, 1, "capability refresh adds no model review or retry")
			require.ElementsMatch(t, tc.tools, currentToolNoticeNames(t, model.calls[0][0].Content))
			require.Equal(t, before, history, "never rewrite an old answer or user restriction")
			require.Equal(t, before, model.calls[0][1:3])
			require.Contains(t, model.calls[0][0].Content, "override user restrictions, permissions")
			require.Empty(t, model.opts[0].RuntimeNotice, "no trailing system message or local ordering retry")
			for _, message := range model.calls[0][1:] {
				require.NotEqual(t, "system", message.Role)
				require.NotContains(t, message.Content, "current_tool_availability", "only the copied system instruction receives the snapshot")
			}
		})
	}
}

func TestCurrentToolsMatchProjectionAndRefreshWithoutAccumulation(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "First", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{Content: "Second", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(newCountingTool("read_document"))
	engine.toolRegistry.RegisterTool(newCountingTool("web_search"))
	engine.taskProjection = &taskToolProjection{selected: map[string]bool{"read_document": true}}
	engine.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{SourceDomains: []string{"knowledge"}}}
	messages := []chat.Message{
		{Role: "system", Content: "Runtime policy"},
		{Role: "user", Content: "Earlier request."},
		{Role: "assistant", Content: "Earlier capability statement."},
		{Role: "user", Content: "Use available sources."},
	}
	before := append([]chat.Message(nil), messages...)
	_, err := engine.streamThinkingToEventBus(t.Context(), messages, engine.buildToolsForLLM(), 0, "session", "pending recovery")
	require.NoError(t, err)
	require.Equal(t, []string{"read_document"}, currentToolNoticeNames(t, model.calls[0][0].Content))
	require.Contains(t, model.opts[0].RuntimeNotice, "pending recovery")
	engine.taskProjection.selected["web_search"] = true
	_, err = engine.streamThinkingToEventBus(t.Context(), messages, engine.buildToolsForLLM(), 1, "session")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"read_document", "web_search"}, currentToolNoticeNames(t, model.calls[1][0].Content))
	require.Equal(t, 1, strings.Count(model.calls[1][0].Content, `"type":"current_tool_availability"`))
	require.NotContains(t, model.opts[1].RuntimeNotice, "pending recovery")
	require.Equal(t, before, messages)
	require.Equal(t, model.calls[0][1:], model.calls[1][1:], "the conversation history remains stable")
	for _, call := range model.calls {
		require.True(t, strings.HasPrefix(call[0].Content, before[0].Content), "preserve the existing system prefix")
	}
}

func TestCurrentToolsKeepFirstTurnProviderOrderingUnchanged(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		Content: "An explanation", Done: true, FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	_, err := engine.Execute(t.Context(), "session", "message", "Explain a general concept.", nil, nil)
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	require.Empty(t, model.opts[0].RuntimeNotice, "a first-turn answer adds no provider system-role fallback")
}

func TestCurrentToolsUseSingleLeadingSystemWithoutProviderRetry(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for i, message := range body.Messages {
			if i > 0 && message.Role == "system" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"System message must be at the beginning."}}`))
				return
			}
		}
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" || !strings.Contains(body.Messages[0].Content, `"type":"current_tool_availability"`) {
			t.Error("missing current capability snapshot in leading system instruction")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client := openaicompletions.New(openaicompletions.Config{Endpoint: api.Endpoint{
		BaseURL: server.URL, Model: "fixture", Auth: api.NoAuth(), Client: server.Client(),
	}})
	engine := newTestEngine(t, client)
	messages := []chat.Message{
		{Role: "system", Content: "Keep the user restrictions."},
		{Role: "user", Content: "Earlier task."},
		{Role: "assistant", Content: "I cannot use external sources."},
		{Role: "user", Content: "Continue with current tools."},
	}
	before := append([]chat.Message(nil), messages...)
	_, err := engine.streamThinkingToEventBus(t.Context(), messages, nil, 0, "session")
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "compatible local endpoint must not receive a failed ordering request first")
	require.Equal(t, before, messages)
}
