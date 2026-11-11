package modelcontext

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type decodedWebSourceBlock struct {
	Offset int    `xml:"offset,attr"`
	Parent *int   `xml:"parent_offset,attr"`
	Text   string `xml:",chardata"`
}

func decodeWebSourceBlocks(t *testing.T, output string) []decodedWebSourceBlock {
	t.Helper()
	var decoded struct {
		Blocks []decodedWebSourceBlock `xml:"source_blocks>block"`
	}
	require.NoError(t, xml.Unmarshal([]byte(output), &decoded))
	return decoded.Blocks
}

func TestWebSourceBlocksKeepSiblingPeriodsAndNestingSeparate(t *testing.T) {
	source := "# Inventory bulletin\n\n" +
		"- ## Period Alpha\n\n  Closing stock: 12 units.\n\n" +
		"- ## Period Beta\n\n  Opening stock: 21 units.\n\n" +
		"  - Verified: 18 units.\n  - Pending: 3 units.\n\n" +
		"  Remarks for Beta.\n\n" +
		"General note for the bulletin.\n\n" +
		"## Period Gamma\n\n| Metric | Value |\n| --- | --- |\n| Stock | 8 |\n"
	var b strings.Builder
	shown := writeWebSourceEvidence(&b, "content", source, 0, 8000, nil)
	require.Equal(t, utf8.RuneCountInString(source), shown)
	blocks := decodeWebSourceBlocks(t, b.String())
	require.Len(t, blocks, 8)
	var reconstructed strings.Builder
	known := map[int]bool{}
	for _, block := range blocks {
		require.Equal(t, utf8.RuneCountInString(reconstructed.String()), block.Offset)
		if block.Parent != nil {
			require.True(t, known[*block.Parent], "parent must be an earlier block: %+v", block)
		}
		known[block.Offset] = true
		reconstructed.WriteString(block.Text)
	}
	require.Equal(t, source, reconstructed.String())
	require.Contains(t, blocks[1].Text, "Period Alpha")
	require.Contains(t, blocks[1].Text, "12 units")
	require.NotContains(t, blocks[1].Text, "21 units")
	require.Contains(t, blocks[2].Text, "Period Beta")
	require.Contains(t, blocks[2].Text, "21 units")
	require.Equal(t, blocks[0].Offset, *blocks[1].Parent)
	require.Equal(t, blocks[0].Offset, *blocks[2].Parent)
	require.Equal(t, blocks[2].Offset, *blocks[3].Parent)
	require.Equal(t, blocks[2].Offset, *blocks[4].Parent)
	require.Equal(t, blocks[2].Offset, *blocks[5].Parent)
	require.Equal(t, blocks[0].Offset, *blocks[6].Parent)
	require.Contains(t, blocks[7].Text, "| Stock | 8 |")
}

func TestWebSourceBlocksDoNotParseCodeOrInventPeriodLabels(t *testing.T) {
	source := "Overview\n========\n\n" +
		"```md\n## Not a heading\n- Not an item\n```\n\n" +
		"##\n\nNo date or period supplied.\n\n" +
		"## Repeated\n\nA < B & C.\n\n## Repeated\n\nD > E.\n"
	var b strings.Builder
	writeWebSourceEvidence(&b, "content", source, 0, 8000, nil)
	blocks := decodeWebSourceBlocks(t, b.String())
	require.Len(t, blocks, 4)
	require.Contains(t, blocks[0].Text, "## Not a heading\n- Not an item")
	require.Equal(t, "##\n\nNo date or period supplied.\n\n", blocks[1].Text)
	require.NotEqual(t, blocks[2].Offset, blocks[3].Offset)
	require.Contains(t, blocks[2].Text, "A < B & C")
	require.NotContains(t, b.String(), "period=")
	require.NotContains(t, b.String(), "date=")
}

func TestWebSourceBlocksBudgetAndUnicodeContinuation(t *testing.T) {
	for _, value := range []string{"ASCII content ", "多字节资料🌱", "<value>&"} {
		t.Run(value, func(t *testing.T) {
			source := "# 报告🌱\n\n- ## Alpha\n\n  " + strings.Repeat(value, 120) +
				"\n\n- ## Beta\n\n  Remaining evidence.\n"
			const limit = 600
			remaining := limit
			var b strings.Builder
			shown := writeWebSourceEvidence(&b, "content", source, 0, limit, &remaining)
			require.Positive(t, shown)
			require.Less(t, shown, utf8.RuneCountInString(source))
			require.LessOrEqual(t, utf8.RuneCountInString(b.String()), limit)
			prefix, _ := truncateModelEvidence(source, limit)
			require.LessOrEqual(t, len(b.String()), max(limit, len(escapeText(prefix))))
			require.Equal(t, limit-utf8.RuneCountInString(b.String()), remaining)
			require.Contains(t, b.String(), `<omitted reason="evidence_budget"`)
			var reconstructed strings.Builder
			for _, block := range decodeWebSourceBlocks(t, b.String()) {
				require.Equal(t, utf8.RuneCountInString(reconstructed.String()), block.Offset)
				reconstructed.WriteString(block.Text)
			}
			require.Equal(t, string([]rune(source)[:shown]), reconstructed.String())
			// Resume with the existing raw Unicode offset, without dropping or
			// repeating any source characters or treating a fragment as a document.
			var next strings.Builder
			tail := string([]rune(source)[shown:])
			consumed := writeWebSourceEvidence(&next, "content", tail, shown, 8000, nil)
			require.Equal(t, utf8.RuneCountInString(tail), consumed)
			require.NotContains(t, next.String(), "<source_blocks")
			require.Contains(t, next.String(), escapeText(tail))
			require.Equal(t, source, reconstructed.String()+tail)
		})
	}
	var resumed strings.Builder
	writeWebSourceEvidence(&resumed, "content", "## inside an earlier fence\n```\n", 30, 1000, nil)
	require.NotContains(t, resumed.String(), "<source_blocks")
}

func TestWebSourceBlocksInvalidUTF8DoesNotCorruptSourceOffsets(t *testing.T) {
	source := "# Record\n\n- Item\n\n  " + string([]byte{0xff, 0xfe}) + " end.\n"
	var b strings.Builder
	shown := writeWebSourceEvidence(&b, "content", source, 0, 8000, nil)
	require.Equal(t, utf8.RuneCountInString(source), shown)
	require.True(t, utf8.ValidString(b.String()))
	blocks := decodeWebSourceBlocks(t, b.String())
	var reconstructed strings.Builder
	for _, block := range blocks {
		reconstructed.WriteString(block.Text)
	}
	require.Equal(t, string([]rune(source)), reconstructed.String())
}

func TestWebSourceBlocksModelViewsPreserveCanonicalDataAndPageIdentity(t *testing.T) {
	source := "# Source record\n\n- ## Alpha\n\n  Value: 12.\n\n- ## Beta\n\n  Value: 21.\n"
	for _, mode := range []string{"search", "fetch"} {
		t.Run(mode, func(t *testing.T) {
			registry := newSourceRegistry()
			rows := []map[string]interface{}{}
			for i := 0; i < 8; i++ {
				rows = append(rows, map[string]interface{}{
					"url": fmt.Sprintf("https://example.com/%d", i), "status": "success",
					"raw_content": source, "offset": 0, "content_length": utf8.RuneCountInString(source),
					"page_verified": true, "page_content": source,
					"snippet": "Provider summary has a separate claim of 99.",
				})
			}
			result := &types.ToolResult{Success: true, Data: map[string]interface{}{
				"display_type": "web_" + mode + "_results", "results": rows,
			}}
			canonical, err := json.Marshal(result.Data)
			require.NoError(t, err)
			output := registry.ModelOutput(result)
			require.Equal(t, 8, strings.Count(output, "<source_blocks"))
			require.Contains(t, output, `page id="url://1"`)
			require.Contains(t, registry.ExpandText(`<ref id="url://1"/>`), `url="https://example.com/0"`)
			after, err := json.Marshal(result.Data)
			require.NoError(t, err)
			require.Equal(t, string(canonical), string(after))
			require.Less(t, utf8.RuneCountInString(output), modelWebFetchTotalMaxRunes+4000)
			if mode == "search" {
				require.Contains(t, output, `<match>Provider summary has a separate claim of 99.</match>`)
				require.NotContains(t, output, `<block offset="0">Provider summary`)
			}
		})
	}
}

func TestWebSourceBlocksSearchAndFetchExposeActualContinuation(t *testing.T) {
	source := "# 原始资料🌱\n\n- ## Alpha\n\n" + strings.Repeat("逐行数据🌱\n", 2200)
	for _, mode := range []string{"search", "fetch"} {
		t.Run(mode, func(t *testing.T) {
			registry := newSourceRegistry()
			row := map[string]interface{}{
				"url": "https://example.com/record", "status": "success",
				"raw_content": source, "content_length": utf8.RuneCountInString(source), "offset": 0,
				"page_verified": true, "page_content": source, "full_output_path": "web://saved",
			}
			output := registry.ModelOutput(&types.ToolResult{Success: true, Data: map[string]interface{}{
				"display_type": "web_" + mode + "_results", "results": []map[string]interface{}{row},
			}})
			var decoded struct {
				Pages []struct {
					Content struct {
						Blocks []decodedWebSourceBlock `xml:"source_blocks>block"`
					} `xml:"content"`
					Fetched struct {
						Blocks []decodedWebSourceBlock `xml:"source_blocks>block"`
					} `xml:"fetched_content"`
					Continue struct {
						URL    string `xml:"url,attr"`
						Offset int    `xml:"next_offset,attr"`
					} `xml:"continue"`
				} `xml:"page"`
			}
			require.NoError(t, xml.Unmarshal([]byte(output), &decoded))
			require.Len(t, decoded.Pages, 1)
			page := decoded.Pages[0]
			blocks := append(page.Content.Blocks, page.Fetched.Blocks...)
			var shown strings.Builder
			for _, block := range blocks {
				shown.WriteString(block.Text)
			}
			require.Equal(t, "url://1", page.Continue.URL)
			require.Equal(t, utf8.RuneCountInString(shown.String()), page.Continue.Offset)
			require.Equal(t, string([]rune(source)[:page.Continue.Offset]), shown.String())
			require.Contains(t, output, `path="web://saved"`)
		})
	}
}
