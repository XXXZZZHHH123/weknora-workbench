package agent

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNumericOccurrencesPreserveOnlyUnmatchedRepeatedPositions(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","rows":[[0.43]]}`}}
	answer := "比例43%，数值43；另一个数值43。"
	first := strings.Index(answer, "数值43") + len("数值")
	last := strings.LastIndex(answer, "43")
	occurrences := ungroundedStructuredNumberOccurrences(answer, messages)
	require.Equal(t, []numericOccurrence{{Token: "43", Start: first, End: first + 2}, {Token: "43", Start: last, End: last + 2}}, occurrences)
	require.Equal(t, []string{"43"}, ungroundedStructuredNumbers(answer, messages), "legacy diagnostics remain deduplicated")
	marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
	require.True(t, ok)
	require.Equal(t, 2, count)
	require.Equal(t, "比例43%，数值43〔数字待核对〕；另一个数值43〔数字待核对〕。", marked)
}

func TestNumericOccurrencesPreserveUTF8OffsetsAcrossProvenanceMasks(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"kind":"structured_source_inspection","status":"ok","hash_verified":true,"source":{"file":"合成表(2).xlsx"},"datasets":[{"literal_fields":[{"metric":"字段37","dimensions":{"类型":"代码53B"}}]}],"ranges":[{"cells":[{"cell":"C74","value":7}]}]}`}}
	answer := "📊 合成表(2).xlsx，字段37、代码53B，第74行、74–74行、B74、3/14。\n" +
		`<kb doc="表格999" chunk_id="abc123" />` + "\n\n" +
		"| 行 | 数值 |\n|---|---|\n| 74 | 999 |\n\n净变动−999。"
	occurrences := ungroundedStructuredNumberOccurrences(answer, messages)
	first, last := strings.Index(answer, "999 |"), strings.LastIndex(answer, "−999")
	require.Equal(t, []numericOccurrence{{Token: "999", Start: first, End: first + 3}, {Token: "−999", Start: last, End: last + len("−999")}}, occurrences)
	marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
	require.True(t, ok)
	require.Equal(t, 2, count)
	require.Contains(t, marked, "| 74 | 999〔数字待核对〕 |")
	require.Contains(t, marked, "净变动−999〔数字待核对〕。")
	require.Equal(t, answer, strings.ReplaceAll(marked, unverifiedNumberMarker, ""))
	require.Equal(t, strings.Count(answer, "|"), strings.Count(marked, "|"), "table delimiters are preserved")
}

func TestNumericAnnotationPreservesImageCitationLinksAndCode(t *testing.T) {
	answer := "![Chart](" + numericChartHandle + ")\n\n" +
		"[7](https://example.test/7)\n\n" +
		`<kb doc="表格999" chunk_id="abc123" />` + "\n\n" +
		"```text\n7\n```\n\n统计999。"
	steps := numericChartSteps()
	occurrences := ungroundedStructuredNumberOccurrences(answer, answerNumericEvidence(answer, steps), steps)
	require.Len(t, occurrences, 1)
	require.Equal(t, "999", occurrences[0].Token)
	marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
	require.True(t, ok)
	require.Equal(t, 1, count)
	require.Equal(t, answer[:occurrences[0].End]+"〔数字待核对〕"+answer[occurrences[0].End:], marked)
}

func TestNumericOccurrencesChartDestinationUsesActualExecutionIdentity(t *testing.T) {
	answer := "![Chart](" + numericChartHandle + ")\n\n统计999。"
	for _, actualRender := range []bool{true, false} {
		t.Run(map[bool]string{true: "resolved render", false: "overridden render name"}[actualRender], func(t *testing.T) {
			steps := numericChartSteps()
			call := &steps[0].ToolCalls[1]
			if actualRender {
				call.Name, call.Target = "provider_proxy", &types.ToolCallTarget{Name: renderChartToolName, Args: call.Args}
			} else {
				call.Target = &types.ToolCallTarget{Name: "external_operation", Args: call.Args}
			}
			occurrences := ungroundedStructuredNumberOccurrences(answer, answerNumericEvidence(answer, steps), steps)
			marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
			if actualRender {
				require.Equal(t, []numericOccurrence{{Token: "999", Start: strings.Index(answer, "999"), End: strings.Index(answer, "999") + 3}}, occurrences)
				require.True(t, ok)
				require.Equal(t, 1, count)
				require.Equal(t, "![Chart]("+numericChartHandle+")\n\n统计999〔数字待核对〕。", marked)
			} else {
				require.Greater(t, len(occurrences), 1, "a render-looking model name cannot exempt another executor's URL")
				require.False(t, ok)
				require.Empty(t, marked)
			}
		})
	}
}

func TestNumericAnnotationFailsClosedForProtectedAndInvalidRanges(t *testing.T) {
	messages := []chat.Message{{Role: "tool", Content: `{"rows":[[7]]}`}}
	for _, answer := range []string{
		"![999](resource://Ab12Cd34Ef56Gh78Ij90Kl)",
		"[999](https://example.test)",
		"[label](https://example.test/999)",
		"[999][reference]\n\n[reference]: https://example.test",
		"<https://example.test/999>",
		"https://example.test/999",
		"`999`",
		"```text\n999\n```",
		"~~~text\n999\n~~~",
		"    999\n",
	} {
		t.Run(answer, func(t *testing.T) {
			occurrences := ungroundedStructuredNumberOccurrences(answer, messages)
			require.NotEmpty(t, occurrences, "the numeric guard continues checking protected structures")
			marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
			require.False(t, ok)
			require.Empty(t, marked)
			require.Zero(t, count)
		})
	}
	for _, test := range []struct {
		answer string
		spans  []numericOccurrence
	}{
		{"值999", []numericOccurrence{{Token: "998", Start: 3, End: 6}}},
		{"值999", []numericOccurrence{{Token: "999", Start: -1, End: 6}}},
		{"值999", []numericOccurrence{{Token: "999", Start: 3, End: 20}}},
		{"值999", []numericOccurrence{{Token: "99", Start: 3, End: 5}, {Token: "99", Start: 4, End: 6}}},
		{`<kb doc="999" />`, []numericOccurrence{{Token: "999", Start: 9, End: 12}}},
	} {
		_, _, ok := annotateUnverifiedNumbers(test.answer, test.spans)
		require.False(t, ok)
	}
}

func TestNumericAnnotationMarksEnclosingOpaqueTokenOnlyOnce(t *testing.T) {
	unknownID := strings.Repeat("a1", 32)
	answer := "未知身份" + unknownID + "，统计999。"
	occurrences := ungroundedStructuredNumberOccurrences(answer, []chat.Message{{Role: "tool", Content: `{"rows":[[7]]}`}})
	marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
	require.True(t, ok)
	require.Equal(t, 2, count)
	require.Equal(t, "未知身份"+unknownID+"〔数字待核对〕，统计999〔数字待核对〕。", marked)
}

func TestNumericAnnotationKeepsUncheckedValueSuffixesIntact(t *testing.T) {
	answer := "合约CF05M，另报05元、-999.5kg、USD999M、2.5e-8和17.4％。"
	messages := []chat.Message{{Role: "tool", Content: `{"rows":[[138.67]],"series":[{"dimensions":{"contract":"CF05M.CZC"}}]}`}}
	occurrences := ungroundedStructuredNumberOccurrences(answer, messages)
	marked, count, ok := annotateUnverifiedNumbers(answer, occurrences)
	require.True(t, ok)
	require.Equal(t, 5, count)
	require.Equal(t, "合约CF05M，另报05〔数字待核对〕元、-999.5kg〔数字待核对〕、USD999M〔数字待核对〕、2.5e-8〔数字待核对〕和17.4％〔数字待核对〕。", marked)
}
