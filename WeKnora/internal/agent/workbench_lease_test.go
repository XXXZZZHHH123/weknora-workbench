package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Source discovery runs before the reasoning loop. A slow provider used to let
// the parent lease expire there, so the loop was fenced with "collaboration
// changed" and the turn ended without calling the model at all.
func TestSlowWorkbenchDiscoveryKeepsParentOwnership(t *testing.T) {
	r, b := engineCollaboration(t)
	r.Lease = 900 * time.Millisecond
	owned, err := r.AcquireParent(t.Context(), b.Scope)
	require.NoError(t, err)
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&input))
		if input.Operation == "describe_tools" {
			_, _ = io.WriteString(w, workbenchDatasetManifest)
			return
		}
		time.Sleep(2 * r.Lease) // a catalogue read slower than one lease
		_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"real-version","source_file":"真实原表.xlsx","is_latest_for_source":true}]}`)
	})
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer, Content: "你好。", Done: true, FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.SetCollaboration(owned)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "s", "m", "你好", []chat.Message{})
	require.NoError(t, err, "a slow source must not fence the parent turn")
	require.Len(t, model.calls, 1, "the model is still asked after slow discovery")
	require.Equal(t, "你好。", state.FinalAnswer)
}
