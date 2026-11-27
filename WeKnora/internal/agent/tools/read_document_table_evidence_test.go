package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const (
	monthlyTableRef = "resource://monthly_fixture_handle"
	annualTableRef  = "resource://annual_fixture_handle_"
	otherTableRef   = "resource://other_fixture_handle__"
)

func tableEvidenceFixture(t *testing.T, page, neighborPage int) (*ReadDocumentTool, *readDocChunkRepo) {
	t.Helper()
	tool, repo := newReadDocumentFixture(6)
	doc := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
	doc.Title, doc.FileName, doc.FileType, doc.FileHash = "Version 2099-12-29", "renamed-2099.pdf", "pptx", "fixture-source"
	repo.ordered[0].Content = "# 第1页\n\nInventory disclosures 2024.3.31"
	repo.ordered[1].Content = fmt.Sprintf("<!-- Slide number: %d -->\n\nPosition by month", page)
	repo.ordered[2].Content = "![Attached source table](" + monthlyTableRef + ")"
	repo.ordered[3].Content = "Position by year\n\n![Attached source table](" + annualTableRef + ")"
	repo.ordered[4].Content = fmt.Sprintf("<!-- Slide number: %d -->\n\n### Position by site\n\n![Other source table](%s)", neighborPage, otherTableRef)
	repo.ordered[5].Content = "### Unrelated\n\n| Day | Forecast |\n|---|---|\n|2024-01-31|844|"
	ocr := func(id, ref string, parent int, content string) *types.Chunk {
		images, err := json.Marshal([]types.ImageInfo{{URL: ref}})
		require.NoError(t, err)
		return &types.Chunk{
			ID: id, TenantID: doc.TenantID, KnowledgeID: doc.ID, KnowledgeBaseID: doc.KnowledgeBaseID,
			ParentChunkID: repo.ordered[parent].ID, ChunkType: types.ChunkTypeImageOCR, Content: content, IsEnabled: true,
			ImageInfo: string(images),
		}
	}
	repo.children = []*types.Chunk{
		ocr("ocr-A", monthlyTableRef, 2, "Units: tonnes\n\nMonth\tStarting\tClosing\tReserve\n2024-01\t15\t23.4\t23.4\n2024-02\t23.4\t24.2\t21"),
		ocr("ocr-B", annualTableRef, 3, "Year\tClosing\tReserve\n2024\t88\t44\n2023\t21\t88"),
		ocr("ocr-C", otherTableRef, 4, "Site\tClosing\tReserve\nNorth\t23.4\t50\nSouth\t11\t23.4"),
	}
	return tool, repo
}

func executeTableEvidenceRead(t *testing.T, ctx context.Context, tool *ReadDocumentTool, args string) *types.ToolResult {
	t.Helper()
	result, err := tool.Execute(ctx, json.RawMessage(args))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	return result
}

func TestReadDocumentTableCandidatesAreVisibleAndRequireActualRead(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, repo := tableEvidenceFixture(t, 6, 27)
	ctx := t.Context()
	candidate := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","query":"Closing Position"}`)
	scope := candidate.Data["table_candidate_scope"].(map[string]any)
	require.Equal(t, true, scope["scan_complete"])
	require.Equal(t, 3, scope["total"])
	require.Equal(t, true, scope["navigation_only"])
	require.Equal(t, 0, candidate.Data["match_count"], "the original literal search does not bind text across chunks")
	require.NotContains(t, candidate.Data, "table_reads")
	require.Len(t, repo.requests, 1, "recall reuses the literal scan's native page")
	rows := candidate.Data["table_candidates"].([]documentTableLocation)
	require.Equal(t, []string{annualTableRef, monthlyTableRef, otherTableRef}, tableTestObjects(rows))
	require.Equal(t, "Position by month", rows[1].Title)
	require.Equal(t, []string{"Month", "Starting", "Closing", "Reserve"}, rows[1].ColumnHeaders)
	require.Equal(t, []string{"2024-01", "2024-02"}, rows[1].RowPeriodLabels)
	require.Equal(t, []string{"2024", "2023"}, rows[0].RowPeriodLabels)
	require.Equal(t, "2024-03-31", rows[1].CoverDate)
	require.Equal(t, "2024.3.31", rows[1].CoverDateLabel)
	require.Equal(t, 1, rows[1].CoverDatePage)
	require.Equal(t, "chunk-0", rows[1].CoverDateChunkID)
	require.Empty(t, rows[1].DisclosureDate, "a date printed on a cover is only the cover date")
	require.Empty(t, rows[1].BodyPointer)
	require.Contains(t, candidate.Output, "table_candidates")
	model := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, candidate)
	for _, label := range []string{"table_candidates", "res://", "Position by month", "Position by year", "2024-01", "Starting", "2024-03-31", "doc://1"} {
		require.Contains(t, model, label)
	}
	require.NotContains(t, model, "23.4", "navigation metadata contains labels and no cell observation")
	require.NotContains(t, model, "table_reads")

	page := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","page":6}`)
	reads := page.Data["table_reads"].([]documentTableLocation)
	require.Equal(t, []string{annualTableRef, monthlyTableRef}, tableTestObjects(reads))
	monthly := reads[1]
	require.Equal(t, "Position by month", monthly.Title)
	require.Equal(t, "Position by year", reads[0].Title)
	require.Equal(t, "chunk-1", monthly.TitleChunkID)
	require.Equal(t, "chunk-2", monthly.BodyChunkID)
	require.Equal(t, "ocr-A", monthly.OCRChunkID)
	require.Equal(t, "tonnes", monthly.Unit)
	require.Equal(t, monthly.BodyPointer, monthly.UnitPointer)
	require.Equal(t, "/data/chunks/0/content", monthly.TitlePointer)
	require.True(t, strings.HasPrefix(monthly.BodyPointer, "/data/image_text_evidence/"))
	require.Empty(t, monthly.DisclosureDate, "cover source text is outside the selected page")
	require.NotContains(t, page.Data, "table_candidates")
	visible := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, page)
	require.Contains(t, visible, "table_reads")
	require.Contains(t, visible, "2024-01\t15\t23.4\t23.4")

	chunk := executeTableEvidenceRead(t, ctx, tool, `{"id":"chunk-2","context":1}`)
	require.Equal(t, []string{annualTableRef, monthlyTableRef}, tableTestObjects(chunk.Data["table_reads"].([]documentTableLocation)))
	require.Equal(t, 6, chunk.Data["chunks"].([]map[string]any)[1]["table_source_page"])

	incomplete := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","page":6,"offset":1,"limit":1}`)
	require.NotContains(t, incomplete.Data, "table_reads", "an offset omitting the title cannot associate the grid with a title")
	paged := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","pages":[1,6]}`)
	require.Equal(t, "/data/chunks/0/content", paged.Data["table_reads"].([]documentTableLocation)[1].CoverDatePointer)
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
	rolledBack := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, candidate)
	require.NotContains(t, rolledBack, "table_candidates")
	require.NotContains(t, rolledBack, "table_candidate_scope")
	rolledBack = modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, page)
	require.NotContains(t, rolledBack, "table_reads")
	require.Contains(t, rolledBack, "2024-01\t15\t23.4\t23.4")
}

func TestReadDocumentTableCandidatePagingStableAcrossReturnedOrder(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, repo := tableEvidenceFixture(t, 43, 12)
	ctx := t.Context()
	first := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","query":"Closing","limit":1}`)
	firstScope := first.Data["table_candidate_scope"].(map[string]any)
	require.Equal(t, true, firstScope["truncated"])
	require.Equal(t, 1, firstScope["next_offset"])
	require.Equal(t, []string{otherTableRef}, tableTestObjects(first.Data["table_candidates"].([]documentTableLocation)))

	slices.Reverse(repo.children)
	slices.Reverse(repo.ordered)
	second := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","query":"Closing","table_candidate_offset":1,"limit":1}`)
	require.Equal(t, []string{annualTableRef}, tableTestObjects(second.Data["table_candidates"].([]documentTableLocation)))
	require.Equal(t, 2, second.Data["table_candidate_scope"].(map[string]any)["next_offset"])
	third := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","query":"Closing","table_candidate_offset":2,"limit":1}`)
	require.Equal(t, []string{monthlyTableRef}, tableTestObjects(third.Data["table_candidates"].([]documentTableLocation)))
	require.Equal(t, false, third.Data["table_candidate_scope"].(map[string]any)["truncated"])
	require.NotContains(t, third.Data, "table_reads")
	require.NotContains(t, third.Data, "query_scope", "candidate continuations do not repeat semantic fallback or chunk reads")

	short := executeTableEvidenceRead(t, WithOutputBudget(ctx, 400), tool, `{"id":"doc-1","query":"Closing","table_candidate_offset":1,"limit":1}`)
	shortScope := short.Data["table_candidate_scope"].(map[string]any)
	require.Equal(t, "output_budget_exhausted", shortScope["status"])
	require.Equal(t, true, shortScope["truncated"])
	require.NotContains(t, shortScope, "next_offset", "do not send the model an identical continuation")
}

func TestReadDocumentTableEvidenceHonorsPagesScopeAndSwitch(t *testing.T) {
	ctx := t.Context()
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", fmt.Sprint(enabled))
			tool, repo := tableEvidenceFixture(t, 9, 10)
			foreign := *repo.children[0]
			foreign.ID, foreign.KnowledgeID, foreign.Content = "foreign", "outside", "Month\tClosing\n2024-01\t848"
			repo.children = append(repo.children, &foreign)
			result := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","page":9,"query":"Closing"}`)
			if !enabled {
				require.NotContains(t, result.Data, "table_candidates")
				require.NotContains(t, result.Data, "table_candidate_scope")
				require.NotContains(t, string(tool.Parameters()), "table_candidate_offset")
				page := executeTableEvidenceRead(t, ctx, tool, `{"id":"doc-1","page":9}`)
				require.NotContains(t, page.Data, "table_reads")
				require.Contains(t, page.Data, types.DocumentImageTextEvidenceKey, "legacy OCR remains available")
				return
			}
			rows := result.Data["table_candidates"].([]documentTableLocation)
			require.Equal(t, []string{annualTableRef, monthlyTableRef}, tableTestObjects(rows))
			for _, row := range rows {
				require.Empty(t, row.DisclosureDate)
				require.Equal(t, 9, row.Page)
			}
			require.Contains(t, string(tool.Parameters()), "table_candidate_offset")
		})
	}

	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, _ := tableEvidenceFixture(t, 9, 10)
	outside := &types.Knowledge{ID: "outside", KnowledgeBaseID: "kb-outside", TenantID: 7}
	tool.knowledgeService.(*readDocKnowledgeService).docs[outside.ID] = outside
	denied, _ := tool.Execute(ctx, json.RawMessage(`{"id":"outside","query":"Closing"}`))
	require.False(t, denied.Success)
	require.NotContains(t, denied.Data, "table_candidates")
}

func TestReadDocumentTableNeverCombinesNeighborsOrTruncatedOCR(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, repo := tableEvidenceFixture(t, 6, 27)
	repo.ordered[3].Content = "![Second independent object](" + annualTableRef + ")"
	ambiguous := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","page":6}`)
	require.Equal(t, []string{monthlyTableRef}, tableTestObjects(ambiguous.Data["table_reads"].([]documentTableLocation)),
		"the second unlabeled figure cannot inherit the title of the preceding object")
	repo.children = repo.children[1:]
	neighbor := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","page":6}`)
	require.NotContains(t, neighbor.Data, "table_reads", "a neighboring OCR return alone cannot inherit the first object's title")
	tool, repo = tableEvidenceFixture(t, 6, 27)
	repo.ordered[3].Content = "![Second independent object](" + annualTableRef + ")"
	navigation := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","page":6,"query":"Closing"}`)
	require.Len(t, navigation.Data["table_candidates"], 2)

	tool, repo = tableEvidenceFixture(t, 6, 27)
	repo.children[0].Content += strings.Repeat("\n2024-03\t15\t91\t70", 1400)
	partial := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","page":6}`)
	require.Equal(t, true, partial.Data["image_text_evidence_truncated"])
	reads := partial.Data["table_reads"].([]documentTableLocation)
	require.Equal(t, []string{annualTableRef}, tableTestObjects(reads))
}

func tableTestObjects(rows []documentTableLocation) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ObjectID)
	}
	return out
}

func TestReadDocumentTableKeepsSameTitleObjectsAndSourcedDates(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, repo := tableEvidenceFixture(t, 3, 27)
	repo.ordered[2].Content = "### Repeated table\n\n![](" + monthlyTableRef + ")\n\n### Repeated table\n\n![](" + annualTableRef + ")"
	repo.children[1].ParentChunkID = repo.ordered[2].ID
	repo.ordered[3].Content = "披露日期：2024年4月1日"
	result := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","pages":[1,3]}`)
	reads := result.Data["table_reads"].([]documentTableLocation)
	require.Equal(t, []string{annualTableRef, monthlyTableRef}, tableTestObjects(reads))
	for _, row := range reads {
		require.Equal(t, "Repeated table", row.Title)
		require.Equal(t, "2024-03-31", row.CoverDate)
		require.Equal(t, "2024-04-01", row.DisclosureDate)
		require.Equal(t, "2024年4月1日", row.DisclosureLabel)
		require.Equal(t, "chunk-3", row.DisclosureChunkID)
		require.Equal(t, "/data/chunks/3/content", row.DisclosurePointer)
	}
	require.Equal(t, reads[0].TitlePointer, reads[1].TitlePointer)
	require.NotEqual(t, reads[0].BodyPointer, reads[1].BodyPointer)
}

func TestReadDocumentTableCandidateRejectsUnverifiedInventory(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	tool, repo := tableEvidenceFixture(t, 3, 27)
	repo.ordered[5] = repo.ordered[0]
	result := executeTableEvidenceRead(t, t.Context(), tool, `{"id":"doc-1","query":"Position"}`)
	require.NotContains(t, result.Data, "table_candidates")
	scope := result.Data["table_candidate_scope"].(map[string]any)
	require.Equal(t, "unavailable", scope["status"])
	require.Equal(t, false, scope["scan_complete"])
	require.NotContains(t, scope, "next_offset")
}
