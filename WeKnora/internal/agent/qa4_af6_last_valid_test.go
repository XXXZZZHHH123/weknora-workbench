package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type af6LastValidTrace struct {
	ID, Question, Final string
	Plan                taskEvidencePlan
	Steps               []types.AgentStep
}

// Replay Af5 KB-16 r1 ev16 (rows), r2 ev17 (count), and the r3 catalog ev8,
// empty rows ev13 and failed latest ev21. The plans are the original planner
// outputs logged in backend-qa4.log. The wire response to the new directed
// rows query comes from the unchanged, successful r1 result contract.
func TestQA4Af6ScopedLastValidReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/qa4/replay/qa4_af6_last_valid.json")
	require.NoError(t, err)
	var traces []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &traces))
	require.Len(t, traces, 3)
	for i, raw := range traces {
		var name struct{ ID string }
		require.NoError(t, json.Unmarshal(raw, &name))
		var baseline string
		for _, variant := range []string{"disabled", "disabled permuted", "enabled", "permuted", "collaboration", "no optional coverage", "no coverage targets", "date from catalog", "explicit binding", "failed count before recovery", "unresolved recovery", "empty recovery", "expired budget", "late resolved read", "report gap only", "catalog only", "no known endpoint date", "last valid disabled", "final read disabled", "revoked"} {
			missing := i == 2
			if !missing && variant != "enabled" && variant != "permuted" && !strings.HasPrefix(variant, "disabled") {
				continue
			}
			t.Run(name.ID+"/"+variant, func(t *testing.T) {
				setAf5StructuredReplayFlags(t, true)
				t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
				if strings.HasPrefix(variant, "disabled") {
					t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "false")
				}
				if variant == "last valid disabled" {
					t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
				}
				if variant == "final read disabled" {
					t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "false")
				}
				trace := decodeAf6LastValidTrace(t, raw)
				good := decodeAf6LastValidTrace(t, traces[0]).Steps[0].ToolCalls[0]
				if strings.Contains(variant, "permuted") {
					permuteAf5StructuredTrace(trace.Steps)
					if missing {
						latest := trace.Steps[2].ToolCalls[0].Result.Data
						for _, fields := range latest["fields"].([]any) {
							slices.Reverse(fields.([]any))
						}
					}
				}
				library := af5ReplayLibrary(t, trace.Steps)
				targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
				var considered []string
				var explicit []any
				if missing {
					original := trace.Steps[1].ToolCalls[0]
					require.Nil(t, original.Args["include_last_valid"])
					require.Nil(t, original.Args["dataset_id"])
					require.Nil(t, original.Args["knowledge_base_id"])
					require.Equal(t, "no_data", original.Result.Data["status"])
					require.Equal(t, "2023-09-22", coverageObject(original.Result.Data["series"])["last_valid_date"])
					require.Nil(t, original.Result.Data["coverage"])
					latest := trace.Steps[2].ToolCalls[0]
					require.Equal(t, "latest", latest.Args["operation"])
					require.Equal(t, "needs_clarification", latest.Result.Data["status"])
					for _, field := range sourceNavigationRows(trace.Steps[0].ToolCalls[0].Result.Data["series"]) {
						considered = append(considered, field["series_id"].(string))
					}
					slices.Sort(considered)
					if variant == "unresolved recovery" {
						good.Result = latest.Result
					}
					if variant == "empty recovery" {
						good.Result = original.Result
					}
					if variant == "explicit binding" {
						explicit = []any{map[string]any{"considered_ids": considered,
							"selected_ids": []string{"futures.close.CF.CZC"}, "reason": "Existing explicit field selection."}}
						original.Args["source_binding"] = explicit
					}
					if variant == "date from catalog" || variant == "no known endpoint date" {
						delete(coverageObject(original.Result.Data["series"]), "last_valid_date")
						rehashLightFixture(t, original.Result.Data)
					}
					if variant == "no known endpoint date" {
						for _, field := range sourceNavigationRows(trace.Steps[0].ToolCalls[0].Result.Data["series"]) {
							delete(field, "last_valid_date")
						}
					}
					if variant == "failed count before recovery" {
						latest.Args["operation"] = "count"
						for _, key := range []string{"dataset_id", "start_date"} {
							latest.Args[key] = good.Args[key]
						}
					}
				}
				if variant == "no optional coverage" {
					trace.Plan.CoverageTargets[0].OnVerifiedGap = nil
					trace.Plan.CoverageTargets[0].Criteria = ""
				}
				if variant == "no coverage targets" {
					trace.Plan.CoverageTargets = nil
				}
				if variant == "report gap only" {
					trace.Plan.CoverageTargets[0].OnVerifiedGap = []string{"report_gap"}
				}
				if variant == "catalog only" {
					trace.Plan.CoverageTargets[0].SourceTime = "catalog_snapshot"
					trace.Plan.CoverageTargets[0].Required = []string{"optional"}
				}
				var reads int
				var binding *collaboration.Binding
				if variant == "collaboration" {
					_, binding = af3CollaborationReplayStore(t)
				}
				registry := af6LastValidRegistry(t, targets, trace.Question, good, considered, explicit, &reads, binding)
				engine, state, model, ctx, messages := prepareAf5StructuredTurn(t, af5StructuredTrace{
					Question: trace.Question, Targets: trace.Plan.CoverageTargets, Steps: trace.Steps,
				}, targets, registry, variant == "revoked")
				if variant == "expired budget" || variant == "late resolved read" {
					ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, time.Now().Add(-time.Second))
				}
				for _, issue := range trace.Plan.CoveragePlanIssues {
					if issue.Target >= 0 && issue.Target < len(trace.Plan.CoverageTargets) {
						trace.Plan.CoverageTargets[issue.Target].PlanIssues = issue.Reasons
					}
				}
				engine.taskPlan = &trace.Plan
				engine.evidenceCompletion = evidenceCompletionTurn{}
				engine.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
				engine.turnReview.Last = candidateDecision{Disposition: candidateAccepted, Candidate: trace.Final,
					CandidateHash: candidateTextDigest(trace.Final), StandardReview: engine.standardAnswerReview(),
					StructuredResultReview: engine.structuredResultReviewEnabled(),
					TaskHash:               engine.candidateTaskDigest(trace.Question, engine.turnReview.TaskConversation),
					EvidenceHash:           reviewDigest(engine.sourceConsistencyMessages(state, engine.turnReview.Conversation)),
					PublicationEvidence:    engine.terminalEvidenceRevision(ctx, state)}
				wantSteps := len(trace.Steps)
				answer, completion := publishAf5StructuredTurn(t, ctx, engine, state, func() {
					if binding != nil {
						engine.SetCollaboration(binding)
						model := &mockChat{responses: []mockResponse{chatAnswer(baseline), discoveryReply("keep", "")}}
						engine.chatModel = model
						outcome, err := engine.runReActIteration(ctx, state, &messages, engine.buildToolsForLLM(), "s", "m", trace.Question, &loopGuards{})
						require.NoError(t, err)
						require.Equal(t, iterOutcomeBreak, outcome, "%s", engine.turnReview.Last.ReasonCode)
						require.Equal(t, 1, model.callCount)
						group, err := binding.Runtime.Store.Read(ctx, binding.Scope)
						require.NoError(t, err)
						require.Equal(t, "completed", group.Status)
						wantSteps++
						return
					}
					engine.commitTerminalResult(ctx, state, "recorded", trace.Final, "", "answer", false)
					if variant == "late resolved read" {
						require.True(t, engine.turnReview.TerminalReceipt.partial)
						state.CurrentRound++
						state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: state.CurrentRound, ToolCalls: []types.ToolCall{good}})
						wantSteps++
					}
				})
				require.Zero(t, model.callCount)
				wantPartial := variant == "unresolved recovery" || variant == "empty recovery" || variant == "expired budget"
				if variant != "revoked" && variant != "final read disabled" {
					require.Equal(t, wantPartial, engine.turnReview.TerminalReceipt.partial)
				}
				if wantPartial {
					require.True(t, completion.HasPartialAnswer())
					require.Equal(t, string(terminalEndpointEvidence), completion.Incomplete.Reason)
					require.Equal(t, terminalNoFailure, engine.turnReview.TerminalReceipt.failure, "partial metadata leaves approval and references intact")
					require.Equal(t, answer, completion.Incomplete.Notice)
					require.Equal(t, baseline, answer, "the partial answer preserves the existing scoped gap")
				} else {
					require.Empty(t, engine.turnReview.TerminalReceipt.partialReason)
				}
				wantRead := missing && !strings.HasPrefix(variant, "disabled") &&
					!slices.Contains([]string{"expired budget", "late resolved read", "catalog only", "no known endpoint date", "last valid disabled", "final read disabled", "revoked"}, variant)
				if wantRead {
					require.Equal(t, 1, reads)
					require.True(t, engine.turnReview.FinalCoverageReadAttempted)
					require.Len(t, state.RoundSteps, wantSteps+1)
					newCall := state.RoundSteps[len(trace.Steps)].ToolCalls[0]
					require.Equal(t, "structured_query_series", newCall.Name)
					require.True(t, newCall.Result.Success, newCall.Result.Error)
					if wantPartial {
						require.Equal(t, good.Result.Data["status"], newCall.Result.Data["status"])
						require.NotContains(t, answer, "17110")
						return
					}
					require.NotContains(t, answer, "unavailable")
				} else {
					require.Zero(t, reads)
					require.False(t, engine.turnReview.FinalCoverageReadAttempted)
					require.Len(t, state.RoundSteps, wantSteps)
					if variant == "disabled" {
						baseline = answer
					} else if !missing || variant == "disabled permuted" {
						require.Equal(t, baseline, answer)
					}
					if missing && variant != "late resolved read" {
						require.NotContains(t, answer, "17110")
						return
					}
				}
				require.Nil(t, completion.Incomplete)
				require.Contains(t, answer, "17110")
				require.Contains(t, answer, "B4705")
				require.Contains(t, answer, "棉花数据库.xlsx")
				require.Regexp(t, `2023(?:\s*年\s*9\s*月\s*22\s*日|-09-22)`, answer)
			})
		}
	}
}

func decodeAf6LastValidTrace(t *testing.T, raw json.RawMessage) af6LastValidTrace {
	t.Helper()
	var trace af6LastValidTrace
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&trace))
	return trace
}

func af6LastValidRegistry(t *testing.T, targets types.SearchTargets, question string, good types.ToolCall, considered []string, explicit []any, reads *int, binding *collaboration.Binding) *agenttools.ToolRegistry {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	t.Setenv("STRUCTURED_DATA_URL", "http://af6-last-valid-replay.invalid")
	registry := agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool().WithSearchTargets(targets)
	base.BindSourceQuestion(question)
	registry.RegisterTool(base)
	registry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, tool := range sourceNavigationRows(manifest["tools"]) {
		if tool["name"] != "query_series" {
			continue
		}
		params := coverageObject(coverageObject(tool["inputSchema"])["properties"])
		for _, property := range []string{"dataset_id", "series_id", "start_date", "end_date", "operation", "source_question"} {
			params[property] = map[string]any{"type": "string"}
		}
		params["limit"] = map[string]any{"type": "integer"}
		params["source_binding"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		params["include_last_valid"] = map[string]any{"type": "boolean"}
	}
	result, err := registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, result.Success)
	previous := http.DefaultTransport
	http.DefaultTransport = af5StructuredTransport(func(request *http.Request) (*http.Response, error) {
		*reads += 1
		if binding != nil {
			require.NoError(t, binding.Runtime.CheckParent(request.Context(), binding.Scope, binding.ParentOwner))
		}
		require.Equal(t, "http://af6-last-valid-replay.invalid/api/tools/read", request.URL.String())
		require.NotEmpty(t, request.Header.Get("X-Research-Signature"))
		encoded, err := base64.RawURLEncoding.DecodeString(request.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(encoded, &identity))
		require.Equal(t, targets[0].KnowledgeBaseID, identity["knowledge_base_id"])
		var call struct {
			Operation string
			Arguments map[string]any
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&call))
		require.Equal(t, "query_series", call.Operation)
		for _, key := range []string{"dataset_id", "series_id", "start_date", "end_date", "operation"} {
			require.Equal(t, good.Args[key], call.Arguments[key], key)
		}
		require.Equal(t, true, call.Arguments["include_last_valid"])
		require.Equal(t, question, call.Arguments["source_question"])
		bindings, ok := call.Arguments["source_binding"].([]any)
		require.True(t, ok, "require explicit groups to prevent a source model call")
		require.Len(t, bindings, 1)
		selection := coverageObject(bindings[0])
		require.ElementsMatch(t, considered, selection["considered_ids"])
		require.Equal(t, []any{good.Args["series_id"]}, selection["selected_ids"])
		if len(explicit) != 0 {
			require.Equal(t, coverageObject(explicit[0])["reason"], selection["reason"])
		}
		body, err := json.Marshal(good.Result.Data)
		require.NoError(t, err)
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return registry
}
