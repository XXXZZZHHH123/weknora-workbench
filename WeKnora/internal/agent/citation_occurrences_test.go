package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCitationAnnotationPreservesExactProseAndTablePositions(t *testing.T) {
	const good = `<kb chunk_id="current" />`
	const bad = `<kb chunk_id="unresolved" />`
	answer := "正文🙂。" + bad + " 接续。" + good + "\n\n| 项目 | 说明 |\n|---|---|\n| A | 数值含义。" + bad + " |"
	marked, count, ok := annotateUnverifiedCitations(answer, map[string]bool{"unresolved": true})
	require.True(t, ok)
	require.Equal(t, 2, count)
	require.Equal(t, strings.ReplaceAll(answer, bad, "〔引用待核对〕"), marked)
	require.Contains(t, marked, good)
	require.Equal(t, strings.Count(answer, "|"), strings.Count(marked, "|"))
}

func TestCitationAnnotationRejectsProtectedOrEmbeddedMarkup(t *testing.T) {
	const tag = `<kb chunk_id="unresolved" />`
	for _, answer := range []string{
		"`" + tag + "`",
		"```xml\n" + tag + "\n```",
		"    " + tag,
		"[" + tag + "](https://example.test)",
		"[example](https://example.test/" + tag + ")",
		"![" + tag + "](resource://AbCdEfGhIjKlMnOpQrStUv)",
		"[" + tag + "][ref]\n\n[ref]: https://example.test",
		`<span title='` + tag + `'>example</span>`,
		`<code>` + tag + `</code>`,
		"<div>\n" + tag + "\n</div>",
	} {
		t.Run(answer, func(t *testing.T) {
			marked, count, ok := annotateUnverifiedCitations(answer, map[string]bool{"unresolved": true})
			require.False(t, ok)
			require.Empty(t, marked)
			require.Zero(t, count)
		})
	}
}

func TestCitationAnnotationBindsEveryRequestedIDAndPreservesGoodExamples(t *testing.T) {
	answer := "正文。<kb chunk_id=\"unresolved\" /> `示例 <kb chunk_id=\"current\" />`"
	marked, count, ok := annotateUnverifiedCitations(answer, map[string]bool{"unresolved": true})
	require.True(t, ok)
	require.Equal(t, 1, count)
	require.Equal(t, "正文。〔引用待核对〕 `示例 <kb chunk_id=\"current\" />`", marked)
	_, _, ok = annotateUnverifiedCitations(answer, map[string]bool{"not-present": true})
	require.False(t, ok)
}
