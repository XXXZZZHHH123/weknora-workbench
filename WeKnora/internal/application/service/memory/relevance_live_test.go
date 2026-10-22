package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This opt-in acceptance test uses a configured development chat model with a
// separate in-memory database containing synthetic memories only. It does not
// load or change any user's memories, and never invokes an image-generation API.
type liveRelevanceModels struct {
	interfaces.ModelService
	model  *types.Model
	client chat.Chat
}

// Logs synthetic selector decisions only; this test never loads user memories.
type liveRelevanceDelegate interface{ chat.Chat }
type liveRelevanceChat struct {
	liveRelevanceDelegate
	t *testing.T
}

func (c *liveRelevanceChat) Chat(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	r, err := c.liveRelevanceDelegate.Chat(ctx, messages, opts)
	if r != nil {
		c.t.Logf("synthetic selection: %s finish=%s", r.Content, r.FinishReason)
	}
	return r, err
}

func (m *liveRelevanceModels) GetChatModel(context.Context, string) (chat.Chat, error) {
	return m.client, nil
}
func (m *liveRelevanceModels) GetModelByID(context.Context, string) (*types.Model, error) {
	return m.model, nil
}
func (m *liveRelevanceModels) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	return nil, errors.New("embedding disabled for this isolated relevance acceptance")
}

func TestMemoryRelevanceLiveModel(t *testing.T) {
	modelPath := os.Getenv("WEKNORA_MEMORY_LIVE_MODEL_FILE")
	if modelPath == "" {
		t.Skip("set a private development model input to run paid chat-model acceptance")
	}
	if keyPath := os.Getenv("WEKNORA_MEMORY_LIVE_KEY_FILE"); keyPath != "" {
		data, err := os.ReadFile(keyPath)
		require.NoError(t, err)
		var values map[string]string
		require.NoError(t, json.Unmarshal(data, &values))
		require.Len(t, values["SYSTEM_AES_KEY"], 32)
		t.Setenv("SYSTEM_AES_KEY", values["SYSTEM_AES_KEY"])
	}
	data, err := os.ReadFile(modelPath)
	require.NoError(t, err)
	var raw struct {
		ID, Name   string
		Type       types.ModelType
		Source     types.ModelSource
		Parameters json.RawMessage
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, types.ModelTypeKnowledgeQA, raw.Type)
	model := &types.Model{ID: raw.ID, TenantID: 1, Name: raw.Name, Type: raw.Type, Source: raw.Source, Status: types.ModelStatusActive}
	require.NoError(t, model.Parameters.Scan([]byte(raw.Parameters)))
	require.NotEmpty(t, model.Parameters.APIKey, "private model input must decrypt in the development process")
	client, err := chat.NewChat(chat.ConfigFromModel(model, "", ""), nil)
	require.NoError(t, err)

	svc, db, tenants := newMemoryHarness(t)
	ctx := enabledCtx(t, tenants, 1, "isolated-relevance-acceptance")
	vectorOff := false
	tenants.set(1, &types.MemoryConfig{Enabled: true, WriteMode: types.MemoryWriteExplicitOnly, VectorRecall: &vectorOff})
	svc.modelService = &liveRelevanceModels{model: model, client: &liveRelevanceChat{liveRelevanceDelegate: client, t: t}}
	scope, err := ResolveScope(ctx)
	require.NoError(t, err)
	_, err = svc.repo.EnsureSubject(ctx, scope)
	require.NoError(t, err)
	fixtures := []struct{ key, kind, content, origin string }{
		{"profile", types.MemoryKindProfile, "The user works as a bicycle mechanic.", types.MemoryOriginExplicit},
		{"quote", types.MemoryKindPreference, "When preparing bicycle repair quotes, list replacement parts separately from labor.", types.MemoryOriginExplicit},
		{"computer", types.MemoryKindFact, "The user's desktop computer has 16 GB of RAM.", types.MemoryOriginExplicit},
		{"cleanup", types.MemoryKindTask, "The user is organizing a weekend volunteer neighborhood cleanup.", types.MemoryOriginExplicit},
		{"photography", types.MemoryKindInterest, "The user regularly studies urban bird photography.", types.MemoryOriginExplicit},
		{"card", types.MemoryKindFact, "The user's library card expires in April.", types.MemoryOriginExtracted},
	}
	ids := map[string]string{}
	for _, fixture := range fixtures {
		id := uuid.NewString()
		ids[fixture.key] = id
		require.NoError(t, db.Create(&types.MemoryItem{ID: id, TenantID: scope.TenantID, SubjectID: scope.SubjectID,
			Kind: fixture.kind, Content: fixture.content, Topic: fixture.key, Origin: fixture.origin,
			Status: types.MemoryStatusActive, Importance: 3, ValidFrom: time.Now()}).Error)
	}
	art := []interfaces.MemoryConversationMessage{
		{Role: "user", Content: "Create a geometric album-cover illustration with blue shapes."},
		{Role: "assistant", Content: "I can adjust its colors and composition."},
	}
	repair := []interfaces.MemoryConversationMessage{
		{Role: "user", Content: "My bicycle needs new brake pads. Prepare a repair quote."},
		{Role: "assistant", Content: "What level of detail would you like in the estimate?"},
	}
	cases := []struct {
		name, query       string
		conversation      []interfaces.MemoryConversationMessage
		required, allowed []string
	}{
		{"unrelated-task-returns-empty", "Make the background lighter.", art, nil, nil},
		{"referring-followup-uses-prior-task", "Use my usual way of structuring that estimate.", repair, []string{"quote"}, []string{"quote", "profile"}},
		{"explicit-topic-change-rejects-old-task", "Make its background lighter.", append(append([]interfaces.MemoryConversationMessage{}, repair...),
			interfaces.MemoryConversationMessage{Role: "user", Content: "Leave the estimate task. I am now designing a geometric album cover."},
			interfaces.MemoryConversationMessage{Role: "assistant", Content: "Let's work on the cover design."}), nil, nil},
		{"current-instruction-overrides-conditional-preference", "For this estimate, combine parts and labor into a single total. Do not list them separately.", repair, nil, []string{"profile"}},
		{"paraphrase-selects-relevant-fact", "Would my workstation handle software that requires at least 24 GB of memory?", nil, []string{"computer"}, []string{"computer"}},
		{"extracted-fact-for-referring-followup", "Will that still be valid then?", []interfaces.MemoryConversationMessage{
			{Role: "user", Content: "I want to visit the library in May using my existing library card."},
			{Role: "assistant", Content: "We should check its expiry date before planning the visit."},
		}, []string{"card"}, []string{"card"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := interfaces.MemoryRecallRequest{ModelID: model.ID, Query: tc.query, Conversation: tc.conversation, SessionID: "isolated"}
			recall := svc.RecallForTurn(interfaces.WithMemoryRecallRequest(ctx, req), req)
			allowed := map[string]bool{}
			for _, key := range tc.allowed {
				allowed[ids[key]] = true
			}
			got := map[string]bool{}
			for _, item := range recall.Items {
				require.True(t, allowed[item.ID], "irrelevant fixture selected: %s", item.Topic)
				got[item.ID] = true
			}
			for _, key := range tc.required {
				require.True(t, got[ids[key]], "relevant fixture missing: %s", key)
			}
			if len(tc.allowed) == 0 {
				require.Empty(t, recall.Prompt)
				require.Empty(t, recall.Items)
			}
			t.Logf("selected=%d required=%d allowed=%d", len(got), len(tc.required), len(tc.allowed))
		})
	}
	t.Run("tool-query-cannot-override-current-conversation", func(t *testing.T) {
		req := interfaces.MemoryRecallRequest{ModelID: model.ID, Query: "Make the background lighter.", Conversation: art, SessionID: "isolated"}
		result := svc.SearchMemory(interfaces.WithMemoryRecallRequest(ctx, req), "bicycle repair quotes", 10)
		require.True(t, result.Available)
		require.Empty(t, result.Items)
	})
}
