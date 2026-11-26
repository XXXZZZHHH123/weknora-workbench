package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQA4GoldLabelCoverage(t *testing.T) {
	var questions map[string][]string
	var labels map[string][]map[string]any
	for path, dest := range map[string]any{"questions.json": &questions, "gold_labels.json": &labels} {
		data, err := os.ReadFile("testdata/qa4/" + path)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, dest), path)
	}
	ids := strings.Fields(`KB-01 KB-02 KB-03 KB-04 KB-05 KB-06 KB-07 KB-08 KB-09 KB-10 KB-11 KB-12 KB-13 KB-14 KB-15 KB-16 KB-17 KB-18 KB-19
		KB-21 KB-22 KB-23 KB-24 KB-25 KB-26 KB-27 KB-28 KB-29 KB-30 KB-31 KB-32 KB-33 KB-34 MT-01 MT-02 MT-03 VG-01 VG-02 VG-03 VG-04 SV-01 SV-02 SV-03`)
	extraTurns := map[string]int{"KB-01": 1, "KB-13": 1, "MT-01": 2, "MT-02": 2, "MT-03": 1}
	require.Len(t, questions, len(ids))
	require.Len(t, labels, len(ids))
	for _, id := range ids {
		require.Len(t, questions[id], 1+extraTurns[id], id)
		require.Len(t, labels[id], len(questions[id]), id)
		for i, label := range labels[id] {
			require.NotEmpty(t, strings.TrimSpace(questions[id][i]), id)
			require.Equal(t, float64(i+1), label["turn"], id)
			require.Equal(t, "original", label["variant"], id)
			require.Equal(t, fmt.Sprintf("questions.json#/%s/%d", id, i), label["input_ref"], id)
			require.NotNil(t, label["required_evidence_roles"], id)
			for _, key := range []string{"expected_goal", "expected_next_action", "expected_delivery", "forbidden_actions"} {
				require.NotEmpty(t, label[key], "%s/%d %s", id, i+1, key)
			}
		}
	}
}
