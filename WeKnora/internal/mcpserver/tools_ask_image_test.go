package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestSummarizeReferencesPreservesImagesOutsideExcerpt(t *testing.T) {
	const ref = "resource://AbCdEfGhIjKlMnOpQrStUv"
	refs := summarizeReferences([]*types.SearchResult{
		nil,
		{
			ID: "chunk", KnowledgeID: "doc", Content: strings.Repeat("长", 400),
			ImageInfo: `[{"url":"` + ref + `","caption":"diagram","original_url":"private-source"},` +
				`{"url":"","original_url":"private-only"}]`,
		},
		{ID: "chunk", KnowledgeID: "doc", ImageInfo: `[{"url":"` + ref + `","ocr_text":"details"}]`},
		{ID: "broken", ImageInfo: `malformed`},
	})
	require.Len(t, refs, 2)
	require.Equal(t, strings.Repeat("长", askExcerptMaxRunes)+"…", refs[0].Excerpt)
	require.Equal(t, []askImage{{URL: ref, Caption: "diagram", OCRText: "details"}}, refs[0].Images)
	require.Empty(t, refs[1].Images)

	cut := summarizeReferences([]*types.SearchResult{{
		ID: "cut", Content: strings.Repeat("x", askExcerptMaxRunes-10) + "![](" + ref + ")",
	}})
	require.Equal(t, strings.Repeat("x", askExcerptMaxRunes-10)+"![](…", cut[0].Excerpt,
		"a reference crossing the limit is dropped, never halved")
	raw, err := json.Marshal(refs)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-source")
	require.NotContains(t, string(raw), "private-only")
}

type imageAskSessionService struct {
	interfaces.SessionService
	refs []*types.SearchResult
}

func (s *imageAskSessionService) CreateSession(_ context.Context, session *types.Session) (*types.Session, error) {
	session.ID = "session-1"
	return session, nil
}

func (s *imageAskSessionService) SetSessionOwnerID(context.Context, uint64, string, string) error {
	return nil
}

func (s *imageAskSessionService) KnowledgeQA(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
	if err := bus.EmitAndWait(ctx, event.Event{
		Type: event.EventAgentReferences, Data: event.AgentReferencesData{References: s.refs},
	}); err != nil {
		return err
	}
	return bus.EmitAndWait(ctx, event.Event{
		Type: event.EventAgentFinalAnswer,
		Data: event.AgentFinalAnswerData{Content: "A textual answer without a Markdown image.", Done: true},
	})
}

type imageAskMessageService struct{ interfaces.MessageService }

func (s *imageAskMessageService) CreateMessage(_ context.Context, message *types.Message) (*types.Message, error) {
	message.ID = "message-" + message.Role
	return message, nil
}

func (s *imageAskMessageService) UpdateMessage(context.Context, *types.Message) error { return nil }

type completionAskSessionService struct {
	imageAskSessionService
	run func(context.Context, *types.QARequest, *event.EventBus) error
}

func (s *completionAskSessionService) AgentQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	return s.run(ctx, req, bus)
}

type completionAskMessageService struct {
	imageAskMessageService
	saved       []types.Message
	interrupted *types.Message
}

func (s *completionAskMessageService) GetMessage(context.Context, string, string) (*types.Message, error) {
	return s.interrupted, nil
}

func (s *completionAskMessageService) UpdateMessage(_ context.Context, message *types.Message) error {
	s.saved = append(s.saved, *message)
	return nil
}

func TestAskPublishesFailureStatusWithoutUncheckedAnswer(t *testing.T) {
	const notice = "The selected period remains unverified."
	const verified = "Verified source reading."
	const interruptedNotice = "The original value was verified. The report check was interrupted."
	for _, scenario := range []struct {
		name, state, content string
		completion           *event.AgentCompleteData
		upstreamError        error
		errorEvent           string
		persisted            bool
	}{
		{name: "typed_incomplete", state: types.MessageRunStateIncomplete, content: notice,
			completion: &event.AgentCompleteData{Incomplete: &event.AgentIncompleteData{Reason: "endpoint_evidence_unverified", Notice: notice}},
			errorEvent: "Earlier retry failed."},
		{name: "no_events", state: types.MessageRunStateIncomplete, content: "the run ended without completed answer content"},
		{name: "empty_complete", state: types.MessageRunStateIncomplete, content: "the run ended without completed answer content", completion: &event.AgentCompleteData{}},
		{name: "upstream_failure", state: types.MessageRunStateIncomplete, content: "upstream unavailable", upstreamError: errors.New("upstream unavailable")},
		{name: "upstream_error_event", state: types.MessageRunStateIncomplete, content: "provider returned no response", errorEvent: "provider returned no response"},
		{name: "interrupted", state: types.MessageRunStateInterrupted, content: context.Canceled.Error(), upstreamError: context.Canceled},
		{name: "persisted_interruption", state: types.MessageRunStateInterrupted, content: interruptedNotice,
			upstreamError: types.ErrMessageRunInterrupted, persisted: true},
		{name: "complete", content: verified, completion: &event.AgentCompleteData{FinalAnswer: verified}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newResourceURLFixture(t)
			sessions := &completionAskSessionService{run: func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
				if scenario.name != "no_events" {
					if err := bus.Emit(ctx, event.Event{
						Type: event.EventAgentFinalAnswer,
						Data: event.AgentFinalAnswerData{Content: "Unchecked draft.", Done: scenario.name == "upstream_failure"},
					}); err != nil {
						return err
					}
				}
				if scenario.errorEvent != "" {
					if err := bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: scenario.errorEvent}}); err != nil {
						return err
					}
				}
				if scenario.completion == nil {
					return scenario.upstreamError
				}
				data := *scenario.completion
				data.MessageID = req.AssistantMessageID
				return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: data})
			}}
			messages := &completionAskMessageService{}
			if scenario.persisted {
				messages.interrupted = &types.Message{ID: "message-assistant", SessionID: "session-1",
					RunState: types.MessageRunStateInterrupted, IsCompleted: true, Content: interruptedNotice}
			}
			f.srv.sessionService, f.srv.messageService = sessions, messages
			f.srv.agentService = &stubAgentService{agents: map[string]*types.CustomAgent{
				types.BuiltinQuickAnswerID: {ID: types.BuiltinQuickAnswerID, IsBuiltin: true,
					Config: types.CustomAgentConfig{AgentMode: types.AgentModeSmartReasoning}},
			}}
			result, err := f.srv.handleAsk(f.ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
				Arguments: map[string]any{"question": "What did the selected source report?"},
			}})
			require.NoError(t, err)
			if scenario.state == "" {
				require.False(t, result.IsError)
				require.Equal(t, verified, result.StructuredContent.(map[string]any)["answer"])
			} else {
				require.True(t, result.IsError)
				require.Empty(t, result.StructuredContent)
				require.Contains(t, result.Content[0].(mcp.TextContent).Text, scenario.content)
				require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "Unchecked draft.")
			}
			if scenario.persisted {
				require.Empty(t, messages.saved)
				require.Equal(t, interruptedNotice, messages.interrupted.Content)
				return
			}
			require.Len(t, messages.saved, 1)
			require.True(t, messages.saved[0].IsCompleted)
			require.Equal(t, scenario.state, messages.saved[0].RunState)
			require.Equal(t, scenario.content, messages.saved[0].Content)
		})
	}
}

func TestAskExposesImageHandlesToTextAndStructuredClients(t *testing.T) {
	f := newResourceURLFixture(t)
	const publicURL = "https://cdn.example/img.png?a=1&b=2"
	info, err := json.Marshal([]types.ImageInfo{{
		URL: f.ref, Caption: "chart", OCRText: "values", OriginalURL: "private-parser-locator",
	}, {URL: publicURL}})
	require.NoError(t, err)
	f.srv.sessionService = &imageAskSessionService{refs: []*types.SearchResult{{
		ID: "chunk", KnowledgeID: "doc-1", Content: strings.Repeat("long text ", 100), ImageInfo: string(info),
	}}}
	f.srv.messageService = &imageAskMessageService{}
	f.srv.agentService = &stubAgentService{agents: map[string]*types.CustomAgent{
		types.BuiltinQuickAnswerID: {ID: types.BuiltinQuickAnswerID, IsBuiltin: true},
	}}
	result, err := f.srv.handleAsk(f.ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"question": "Explain the diagram"},
	}})
	require.NoError(t, err)
	require.False(t, result.IsError, "%#v", result.Content)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, `Image: "`+f.ref+`"`)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, `Image: "`+publicURL+`"`)
	result = f.rewrite(result)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var parsed struct {
		References []askReference `json:"references"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))
	require.Len(t, parsed.References, 1)
	require.Len(t, parsed.References[0].Images, 2)
	require.Equal(t, publicURL, parsed.References[0].Images[1].URL)
	img := parsed.References[0].Images[0]
	require.True(t, strings.HasPrefix(img.URL, "https://weknora.example/prefix/r/"))
	require.Equal(t, "chart", img.Caption)
	require.Equal(t, "values", img.OCRText)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, img.URL)
	require.NotContains(t, string(raw), "private-parser-locator")
	require.NotContains(t, string(raw), "resource://")
}
