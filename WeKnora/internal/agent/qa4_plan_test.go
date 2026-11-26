package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type qa4PlanSample struct {
	Case     string
	Turn     int
	Domains  []string
	Target   map[string]any
	mutation bool
}

func readQA4Fixture[T any](t *testing.T, name string) (value T) {
	t.Helper()
	raw, err := os.ReadFile("testdata/qa4/" + name + ".json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

func qa4PlanReply(t *testing.T, target map[string]any, domains ...string) string {
	t.Helper()
	if len(domains) == 0 {
		domains = []string{"knowledge"}
	}
	target = maps.Clone(target)
	target["goal_index"] = 0
	return fmt.Sprintf(`{"protocol_version":1,"goals":["Satisfy the requested scope."],"evidence":["Actual source records."],"tool_names":[],"completion_criteria":["Provide attributable observations."],"source_discovery":{"mode":"recover","source_domains":%s},"delivery_requirements":[{"kind":"text","format":"prose","criteria":"Preserve the requested scope."}],"coverage_targets":[%s]}`, mustJSON(t, domains), mustJSON(t, target))
}

func TestQA4StructuredPlanning(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "")
	questions := readQA4Fixture[map[string][]string](t, "questions")
	labels := readQA4Fixture[map[string][]map[string]any](t, "gold_labels")
	rewrites := readQA4Fixture[map[string]struct{ Question string }](t, "rewrite_questions")
	rewriteLabels := readQA4Fixture[map[string]map[string]any](t, "rewrite_labels")
	samples := readQA4Fixture[map[string]qa4PlanSample](t, "constructed_plans")
	for id, turns := range labels {
		for index, label := range turns {
			// Check expressibility independently of the scripted planner.
			target := maps.Clone(label["expected_goal"].(map[string]any))
			target["request_basis"] = questions[id][index]
			if target["source_mode"] == "only" {
				target["restriction_basis"] = target["request_basis"]
			}
			_, err := decodeTaskEvidencePlan(qa4PlanReply(t, target), nil, 0)
			require.NoError(t, err, "%s/%d", id, index)
		}
	}
	for name := range rewrites {
		require.Contains(t, samples, name)
	}
	base := samples["warehouse_sheet_only"]
	edits := readQA4Fixture[map[string]map[string]any](t, "plan_mutations")
	edits["oversized_metric"] = map[string]any{"metric": strings.Repeat("界", 513)}
	for name, edit := range edits {
		sample := base
		sample.Target, sample.mutation = maps.Clone(base.Target), true
		maps.Copy(sample.Target, edit)
		samples[name] = sample
	}
	for name, sample := range samples {
		t.Run(name, func(t *testing.T) {
			history := questions[sample.Case]
			question, label := history[sample.Turn], labels[sample.Case][sample.Turn]
			if rewrite, ok := rewrites[name]; ok {
				question, label = rewrite.Question, rewriteLabels[name]
			}
			if sample.mutation {
				question = rewrites["warehouse_sheet_only"].Question
			}
			legacy, disabled := name == "off_old", strings.HasPrefix(name, "off_")
			if disabled {
				t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "false")
			}
			reply := qa4PlanReply(t, sample.Target, sample.Domains...)
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: reply}}
			switch name {
			case "off_old":
				model.plan.Content = validTaskPlan(t, "read_document")
			case "unavailable":
				model.plan = nil
			}
			e, state, _ := taskPlanFixture(t, model)
			e.config.TaskPlanningHumanQuestion = &question
			if name == "KB-03" || name == "selected_outside_scope" {
				for _, id := range []string{"selected-report", "outside-report"} {
					e.selectedDocs = append(e.selectedDocs, &SelectedDocumentInfo{KnowledgeID: id, KnowledgeBaseID: "selected-base", FileName: id + ".pdf"})
					e.modelContext.RegisterDocument(id)
				}
				e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "selected-base",
					KnowledgeIDs: []string{"selected-report"}, TagIDs: []string{"report-tag"}}}
			}
			query := question
			messages := []chat.Message{{Role: "system", Content: e.buildSystemPrompt(t.Context())}}
			if sample.mutation {
				query += " Rendered-only restriction. doc://9999"
				messages = append(messages,
					chat.Message{Role: "user", Content: "Attached limitation.", HumanContent: new("Earlier authorized request.")},
					chat.Message{Role: "assistant", Content: "Assistant-only restriction."},
					chat.Message{Role: "user", Kind: chat.MessageKindReviewEvidence, Content: "Runtime-only restriction."},
					chat.Message{Role: "user", Content: "Unmarked user restriction."})
			}
			for _, earlier := range history[:sample.Turn] {
				messages = append(messages, chat.Message{Role: "user", Content: earlier, HumanContent: &earlier})
			}
			planned := e.planWorkbenchTask(t.Context(), state, query, messages, "qa4")
			require.Len(t, model.requests, 1)
			require.Equal(t, !disabled, gjson.GetBytes(model.options[0].Format, "properties.protocol_version").Exists())
			require.Equal(t, !disabled, strings.Contains(planned[0].Content, "计划缺少有效范围声明"))
			if sample.mutation {
				require.Equal(t, legacy, strings.Contains(planned[0].Content, "<advisory_task_plan>"))
				require.Equal(t, legacy, e.taskPlan != nil)
				return
			}
			projected := string(mustJSON(t, e.taskPlan))
			require.Contains(t, planned[0].Content, projected+"\n</advisory_task_plan>")
			require.JSONEq(t, reply, projected)
			if name == "KB-03" {
				scope := gjson.Get(model.requests[0][1].Content, "runtime_scope").String()
				require.Contains(t, scope, `knowledge_id="doc://1"`)
				require.NotContains(t, scope, "outside-report.pdf")
			}
			goal := gjson.ParseBytes(mustJSON(t, label["expected_goal"]))
			got := gjson.Get(projected, "coverage_targets.0")
			for _, key := range []string{"source_mode", "source_time", "version_task", "delivery.kind", "delivery.aggregation", "period.grain"} {
				require.Equal(t, goal.Get(key).String(), got.Get(key).String(), key)
			}
			if count := map[string]int64{"KB-09": 4, "MT-01-followup": 2}[name]; count > 0 {
				require.Equal(t, count, got.Get("period.count").Int())
			}
			if name == "KB-06" {
				for _, metric := range []string{"主力", "CF09", "收盘价"} {
					require.Contains(t, strings.ReplaceAll(goal.Get("metric").String(), "主连", "主力"), metric)
					require.Contains(t, got.Get("metric").String(), metric)
				}
			}
		})
	}
}
