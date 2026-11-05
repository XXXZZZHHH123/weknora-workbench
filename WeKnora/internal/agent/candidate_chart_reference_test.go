package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const chartReferenceFixture = "resource://abcdefghijklmnopqrstuv"

func chartReferenceState() *types.AgentState {
	return &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "current-render", Name: renderChartToolName, Args: map[string]any{"operation": "render"},
		Result: &types.ToolResult{Success: true, Output: "![Chart](" + chartReferenceFixture + ")", Data: map[string]any{
			"kind": "data_chart", "status": "ok", "artifact": map[string]any{"handle": chartReferenceFixture},
		}},
	}}}}}
}

func TestChartDeliveryUsesCurrentImageIdentityInsteadOfOutputPresentation(t *testing.T) {
	for name, candidate := range map[string]string{
		"original output":           "![Chart](" + chartReferenceFixture + ")",
		"different descriptive alt": "![Observed series](" + chartReferenceFixture + ")",
		"Unicode alt":               "![观测图 İ](" + chartReferenceFixture + ")",
		"angle destination":         "![Observed series](<" + chartReferenceFixture + ">)",
		"double quoted title":       "![Observed series](" + chartReferenceFixture + ` "Saved figure")`,
		"single quoted title":       "![Observed series](" + chartReferenceFixture + ` 'Saved figure')`,
		"parenthesized title":       "![Observed series](" + chartReferenceFixture + " (Saved figure))",
		"prose around image":        "Observed result:\n\n![Observed series](" + chartReferenceFixture + ")\n\nSource limitations follow.",
		"visible quoted image":      "> ![Observed series](" + chartReferenceFixture + ")",
	} {
		t.Run(name, func(t *testing.T) {
			state := chartReferenceState()
			state.RoundSteps[0].ToolCalls[0].Result.Output += "\nThe tool also returned process text."
			require.True(t, chartIncludedInCandidate(candidate, state))
		})
	}
	state := chartReferenceState()
	state.RoundSteps[0].ToolCalls[0].Result.Data["artifact"] = struct {
		Handle string `json:"handle"`
	}{Handle: chartReferenceFixture}
	require.True(t, chartIncludedInCandidate("![Typed live delivery]("+chartReferenceFixture+")", state))
}

func TestChartDeliveryRejectsNonImageAndCodeExamples(t *testing.T) {
	image := "![Observed series](" + chartReferenceFixture + ")"
	for name, candidate := range map[string]string{
		"plain URI":                  "The image identifier is " + chartReferenceFixture,
		"ordinary link":              "[Download figure](" + chartReferenceFixture + ")",
		"escaped image":              `\` + image,
		"escaped closing label":      `![Observed series\](` + chartReferenceFixture + ")",
		"nested image alt":           "![outer " + image + "](https://example.invalid/outer.png)",
		"multiline nested image alt": "![outer\n" + image + "](https://example.invalid/outer.png)",
		"reference definition":       "[figure]: " + image,
		"complex link title":         `[link](https://example.invalid/a(b) "` + image + `")`,
		"URI autolink body":          "See <https://example.invalid/" + image + ">",
		"GFM URL body":               "See https://example.invalid/" + image,
		"GFM www body":               "See www.example.invalid/" + image,
		"inline code":                "`" + image + "`",
		"long inline delimiter":      "``" + image + "``",
		"multiline inline code":      "`example\n" + image + "\nexample`",
		"backtick fence":             "```markdown\n" + image + "\n```",
		"tilde fence":                "~~~\n" + image + "\n~~~",
		"unclosed fence":             "```\n" + image,
		"quoted fence":               "> ```\n> " + image + "\n> ```",
		"list fence":                 "- ```\n  " + image + "\n  ```",
		"ordered list fence":         "1. ```\n   " + image + "\n   ```",
		"nested list quote fence":    "- > ```\n  > " + image + "\n  > ```",
		"indented code":              "    " + image,
		"tab code":                   "\t" + image,
		"mixed space tab code":       " \t" + image,
		"quoted indented code":       ">     " + image,
		"HTML comment":               "<!--\n\n" + image + "\n-->",
		"raw code element":           "<pre>\n\n" + image + "\n</pre>",
		"inline code element":        "Example <code>" + image + "</code>",
		"HTML attribute":             `Example <span title="` + image + `">example</span>`,
		"quoted angle in attribute":  `Example <span title="> ` + image + `">example</span>`,
		"other handle":               "![Observed series](resource://zyxwvutsrqponmlkjihgfe)",
		"handle suffix":              "![Observed series](" + chartReferenceFixture + "_extra)",
		"query suffix":               "![Observed series](" + chartReferenceFixture + "?download=1)",
		"different scheme":           "![Observed series](https://example.invalid/figure.png)",
		"missing image":              "The values are shown in the table.",
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, chartIncludedInCandidate(candidate, chartReferenceState()))
		})
	}
	require.True(t, chartIncludedInCandidate("`example`\n\n"+image, chartReferenceState()), "code elsewhere does not hide the actual image")
}

func TestChartDeliveryNeedsCurrentSuccessfulArtifactIdentity(t *testing.T) {
	candidate := "![Observed series](" + chartReferenceFixture + ")"
	for name, mutate := range map[string]func(*types.ToolCall){
		"failed execution": func(call *types.ToolCall) { call.Result.Success = false },
		"missing result":   func(call *types.ToolCall) { call.Result = nil },
		"other tool":       func(call *types.ToolCall) { call.Name = "unrelated_tool" },
		"not chart data":   func(call *types.ToolCall) { call.Result.Data["kind"] = "chart_source_catalog" },
		"not successful":   func(call *types.ToolCall) { call.Result.Data["status"] = "error" },
		"missing identity": func(call *types.ToolCall) { delete(call.Result.Data, "artifact") },
		"empty identity":   func(call *types.ToolCall) { call.Result.Data["artifact"] = map[string]any{"handle": ""} },
		"invalid identity": func(call *types.ToolCall) {
			call.Result.Data["artifact"] = map[string]any{"handle": chartReferenceFixture + "_extra"}
		},
		"different saved identity": func(call *types.ToolCall) {
			call.Result.Data["artifact"] = map[string]any{"handle": "resource://zyxwvutsrqponmlkjihgfe"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := chartReferenceState()
			mutate(&state.RoundSteps[0].ToolCalls[0])
			require.False(t, chartIncludedInCandidate(candidate, state))
		})
	}
	require.False(t, chartIncludedInCandidate(candidate, nil))
	require.False(t, chartIncludedInCandidate(candidate, &types.AgentState{}), "history alone cannot register a current rendered chart")
}

func TestChartIdentityStillRequiresDeliveredReviewAndSemanticApproval(t *testing.T) {
	candidate := "![Observed series](" + chartReferenceFixture + ")"
	e := sourceConsistencyEngine(t, &mockChat{})
	e.taskPlan = chartDeliveryPlan()
	repair, err := e.validateDeliveryCheck(&answerDeliveryCheck{Status: "delivered", Assessment: "Checked the actual selected data and requested dimensions."}, candidate, chartReferenceState())
	require.NoError(t, err)
	require.False(t, repair)
	repair, err = e.validateDeliveryCheck(&answerDeliveryCheck{Status: "missing", Assessment: "The requested dimension is not delivered."}, candidate, chartReferenceState())
	require.NoError(t, err)
	require.True(t, repair)
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The actual result does not support the requested dimension.","next_action":"revise_answer"}`)}}
	e = sourceConsistencyEngine(t, model)
	e.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	state := &types.AgentState{RoundSteps: numericChartSteps()}
	decision := e.checkCandidate(t.Context(), "Use the requested observations.", "![Descriptive figure label]("+numericChartHandle+")", "", state, nil, false)
	require.Equal(t, candidateRevise, decision.Disposition)
	require.Len(t, model.calls, 1, "a matching resource cannot bypass the independent source critic")
}
