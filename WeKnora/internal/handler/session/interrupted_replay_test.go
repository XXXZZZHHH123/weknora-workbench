package session

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestInterruptedReplayUsesPersistedVerifiedResult(t *testing.T) {
	raw, err := os.ReadFile("../../agent/testdata/qa4/replay/af1-kb28-r2-interruption.json")
	require.NoError(t, err)
	var replay struct {
		HistoricalFinal string `json:"historical_final"`
	}
	require.NoError(t, json.Unmarshal(raw, &replay))
	message := &types.Message{RunState: types.MessageRunStateInterrupted, Content: "已核实的来源读数（取值格 G87）。\n\n本轮执行中断，查证未完成。"}
	for _, kind := range []types.ResponseType{types.ResponseTypeError, types.ResponseTypeComplete} {
		event := interfaces.StreamEvent{Type: kind, Done: true, Content: replay.HistoricalFinal, Data: map[string]any{"final_content": replay.HistoricalFinal}}
		result := interruptedReplayEvent(t.Context(), event, message)
		require.Equal(t, kind, result.Type)
		require.True(t, result.Done)
		require.Equal(t, message.Content, result.Content)
		require.Equal(t, message.Content, result.Data["final_content"])
		require.Equal(t, false, result.Data["success"])
		require.Equal(t, replay.HistoricalFinal, event.Data["final_content"], "the replay must not mutate the cached event")
	}
	ordinary := interfaces.StreamEvent{Type: types.ResponseTypeComplete, Content: "complete result", Done: true}
	require.Equal(t, ordinary, interruptedReplayEvent(t.Context(), ordinary, nil))
}
