package session

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type turnMemoryWrites struct {
	interfaces.MemoryService
	explicit, extracted, sources int
}

func (s *turnMemoryWrites) Remember(_ context.Context, item types.MemoryItem) (*types.MemoryItem, error) {
	s.explicit++
	return &item, nil
}

func (s *turnMemoryWrites) ScheduleExtraction(context.Context, string, string, string) {
	s.extracted++
}

func (s *turnMemoryWrites) RecordAnswerSources(context.Context, []types.MemoryDocAffinity) {
	s.sources++
}

func TestTemporaryConversationBlocksExplicitAndAsyncWritesAfterRequestCloses(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "temporary"}[temporary], func(t *testing.T) {
			memory := &turnMemoryWrites{}
			h := &Handler{memoryService: memory}
			ctx, cancel := context.WithCancel(context.Background())
			ctx = types.ApplySessionMemoryPreference(ctx, &types.Session{MemoryDisabled: temporary})
			enabled := true
			ctx = types.ApplyAgentMemoryPreference(ctx, &enabled)
			cancel()
			// Completion dispatches this work with WithoutCancel after the HTTP
			// request ends. A new agent cannot reopen the session's memory policy.
			background := context.WithoutCancel(ctx)
			require.NoError(t, background.Err())
			h.recordTurnMemory(background, &types.Message{ID: "answer", SessionID: "conversation",
				KnowledgeReferences: types.References{{KnowledgeID: "source", KnowledgeBaseID: "library"}}},
				"remember: Keep this isolated fixture", "user-message")
			if temporary {
				require.Zero(t, memory.explicit)
				require.Zero(t, memory.extracted)
				require.Zero(t, memory.sources)
			} else {
				require.Equal(t, 1, memory.explicit)
				require.Equal(t, 1, memory.extracted)
				require.Equal(t, 1, memory.sources)
			}
		})
	}
}
