package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMemoryRecalledClearSurvivesMessageSaveAndReload(t *testing.T) {
	for _, empty := range []types.UsedMemories{nil, {}} {
		t.Run(fmt.Sprintf("nil=%v", empty == nil), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			ctx := context.Background()
			oldUsed := types.UsedMemories{{ID: "older-memory", Kind: "fact", Content: "Earlier turn"}}
			old := &types.Message{ID: "older", SessionID: "session", Role: "assistant", UsedMemories: oldUsed}
			current := &types.Message{ID: "current", SessionID: "session", Role: "assistant", UsedMemories: oldUsed}
			require.NoError(t, db.Create(old).Error)
			require.NoError(t, db.Create(current).Error)
			stream := &capturingStreamManager{}
			h := NewAgentStreamHandler(ctx, "session", current.ID, "request", 7, time.Time{}, current, stream, nil, nil, nil, nil)
			require.NoError(t, h.handleMemoryRecalled(ctx, event.Event{Data: event.MemoryRecalledData{Memories: empty}}))
			require.NotNil(t, current.UsedMemories, "an explicit empty must survive GORM struct updates")
			require.Empty(t, current.UsedMemories)
			require.Len(t, stream.events, 1)
			require.Equal(t, types.ResponseTypeMemoryRecalled, stream.events[0].Type)
			wire, err := json.Marshal(stream.events[0].Data)
			require.NoError(t, err)
			require.JSONEq(t, `{"memories":[]}`, string(wire))
			repo := repository.NewMessageRepository(db)
			require.NoError(t, repo.UpdateMessage(ctx, current))
			reloaded, err := repo.GetMessage(ctx, "session", current.ID)
			require.NoError(t, err)
			require.Empty(t, reloaded.UsedMemories)
			older, err := repo.GetMessage(ctx, "session", old.ID)
			require.NoError(t, err)
			require.Equal(t, oldUsed, older.UsedMemories, "a current-turn clear must leave older rows intact")
		})
	}
}

func TestMemoryRecalledMergePublishesFullListAndReplaceClearsIt(t *testing.T) {
	ctx := context.Background()
	stream := &capturingStreamManager{}
	current := &types.Message{}
	h := NewAgentStreamHandler(ctx, "session", "current", "request", 7, time.Time{}, current, stream, nil, nil, nil, nil)
	a := types.UsedMemory{ID: "a", Content: "First read"}
	b := types.UsedMemory{ID: "b", Content: "Tool read"}
	for _, step := range []struct {
		input types.UsedMemories
		merge bool
		want  types.UsedMemories
	}{
		{types.UsedMemories{a}, false, types.UsedMemories{a}},
		{types.UsedMemories{a, b}, true, types.UsedMemories{a, b}},
		{nil, true, types.UsedMemories{a, b}},
		{nil, false, types.UsedMemories{}},
		{types.UsedMemories{b}, true, types.UsedMemories{b}},
	} {
		require.NoError(t, h.handleMemoryRecalled(ctx, event.Event{Data: event.MemoryRecalledData{Memories: step.input, Merge: step.merge}}))
		require.Equal(t, step.want, current.UsedMemories)
		require.Equal(t, step.want, stream.events[len(stream.events)-1].Data["memories"], "the client replaces its list with this complete snapshot")
		if len(step.input) > 0 {
			step.input[0].Content = "Caller mutation after delivery"
			require.Equal(t, step.want, current.UsedMemories, "the handler owns its delivered snapshot")
		}
	}
	require.Len(t, stream.events, 5)
	require.NoError(t, h.handleMemoryRecalled(ctx, event.Event{Data: event.MemoryRecalledData{Memories: "malformed"}}))
	require.Len(t, stream.events, 5)
}

func TestMemoryRecalledConcurrentToolMergesKeepEveryRead(t *testing.T) {
	ctx := context.Background()
	stream := &capturingStreamManager{}
	current := &types.Message{}
	h := NewAgentStreamHandler(ctx, "session", "current", "request", 7, time.Time{}, current, stream, nil, nil, nil, nil)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.handleMemoryRecalled(ctx, event.Event{Data: event.MemoryRecalledData{
				Memories: types.UsedMemories{{ID: fmt.Sprintf("memory-%d", i)}}, Merge: true,
			}})
		}()
	}
	wg.Wait()
	require.Len(t, current.UsedMemories, 12)
	require.Len(t, stream.events, 12)
	for i, evt := range stream.events {
		require.Len(t, evt.Data["memories"], i+1, "stream snapshots preserve the serialized merge order")
	}
}
