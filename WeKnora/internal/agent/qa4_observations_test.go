package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func assertQA4ReplayObservations(t *testing.T, id string, raw []byte, engine *AgentEngine, steps []types.AgentStep) {
	t.Helper()
	var expected map[string]struct {
		CompareReadPrefix int `json:"compare_read_prefix"`
		Checks            []struct {
			Path          string
			Equal         json.RawMessage
			Contains      []string
			Omits         []string
			SameAs        string `json:"same_as"`
			DifferentFrom string `json:"different_from"`
		}
	}
	content, err := os.ReadFile("testdata/qa4/replay_expectations.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(content, &expected))
	spec := expected[id]
	require.NotEmpty(t, spec.Checks, id)
	state := &types.AgentState{RoundSteps: steps}
	_, progress := engine.sourceExecutionProgress(state)
	view := map[string]any{"history": json.RawMessage(raw), "progress": progress, "reads": engine.discoveryReadObservations(state)}
	if spec.CompareReadPrefix > 0 {
		require.LessOrEqual(t, spec.CompareReadPrefix, len(steps))
		view["prefix_reads"] = engine.discoveryReadObservations(&types.AgentState{RoundSteps: steps[:spec.CompareReadPrefix]})
	}
	projected, err := json.Marshal(view)
	require.NoError(t, err)
	for _, check := range spec.Checks {
		actual := gjson.GetBytes(projected, check.Path)
		require.True(t, actual.Exists(), "%s is missing", check.Path)
		if check.Equal != nil {
			require.JSONEq(t, string(check.Equal), actual.Raw, check.Path)
		}
		if check.SameAs != "" {
			require.JSONEq(t, gjson.GetBytes(projected, check.SameAs).Raw, actual.Raw, check.Path)
		}
		if check.DifferentFrom != "" {
			other := gjson.GetBytes(projected, check.DifferentFrom)
			require.True(t, other.Exists(), "%s is missing", check.DifferentFrom)
			require.NotEqual(t, other.Value(), actual.Value(), check.Path)
		}
		for _, text := range check.Contains {
			require.Contains(t, actual.String(), text, check.Path)
		}
		for _, text := range check.Omits {
			require.NotContains(t, actual.String(), text, check.Path)
		}
	}
}
