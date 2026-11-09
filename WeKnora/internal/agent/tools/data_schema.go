package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

var dataSchemaTool = BaseTool{
	name: ToolDataSchema,
	description: "Use this tool to get the schema information of a CSV or Excel file loaded into DuckDB. " +
		"It returns the columns and row count. When querying the document with data_analysis, " +
		"reference it as the table \"" + DataAnalysisTableName + "\". " +
		"Only available for documents with an existing DuckDB table schema. An unavailable schema does not mean the original file has no data; use the provided structured-source catalog or read_document instead of repeating this call.",
	schema: utils.GenerateSchema[DataSchemaInput](),
}

type DataSchemaInput struct {
	KnowledgeID string `json:"knowledge_id" jsonschema:"doc://N document handle to query"`
}

type DataSchemaTool struct {
	BaseTool
	knowledgeService interfaces.KnowledgeService
	chunkRepo        interfaces.ChunkRepository
	targetChunkTypes []types.ChunkType
	searchTargets    types.SearchTargets
	scopeEnforced    bool
}

// WithSearchTargets enables Agent request-scope authorization. An Agent turn
// with no search target must reject every document rather than fall back to
// the unrestricted service-owned lookup.
func (t *DataSchemaTool) WithSearchTargets(searchTargets types.SearchTargets) *DataSchemaTool {
	t.searchTargets = searchTargets
	t.scopeEnforced = true
	return t
}

func NewDataSchemaTool(knowledgeService interfaces.KnowledgeService, chunkRepo interfaces.ChunkRepository, targetChunkTypes ...types.ChunkType) *DataSchemaTool {
	if len(targetChunkTypes) == 0 {
		targetChunkTypes = []types.ChunkType{types.ChunkTypeTableSummary, types.ChunkTypeTableColumn}
	}
	return &DataSchemaTool{
		BaseTool:         dataSchemaTool,
		knowledgeService: knowledgeService,
		chunkRepo:        chunkRepo,
		targetChunkTypes: targetChunkTypes,
	}
}

// Execute executes the tool logic
func (t *DataSchemaTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var input DataSchemaInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to parse input args: %v", err),
		}, err
	}

	// Get knowledge to get TenantID (use IDOnly to support cross-tenant shared KB)
	var knowledge *types.Knowledge
	var err error
	if t.scopeEnforced {
		knowledge, err = authorizeKnowledgeInSearchTargets(ctx, t.searchTargets, input.KnowledgeID, t.knowledgeService)
	} else {
		knowledge, err = t.knowledgeService.GetKnowledgeByIDOnly(ctx, input.KnowledgeID)
	}
	if err != nil || knowledge == nil {
		if err == nil {
			err = fmt.Errorf("knowledge service returned an empty result")
		}
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to get knowledge '%s': %v", input.KnowledgeID, err),
		}, err
	}

	// Get chunks for the knowledge ID using ChunkRepository
	// We only need table summary and column chunks
	chunkTypes := t.targetChunkTypes
	page := &types.Pagination{
		Page:     1,
		PageSize: 100, // Should be enough for schema chunks
	}
	enabled := true

	chunks, _, err := t.chunkRepo.ListPagedChunksByKnowledgeID(
		ctx,
		knowledge.TenantID,
		input.KnowledgeID,
		page,
		chunkTypes,
		nil, // tagIDs
		"",  // keyword
		"",  // searchField
		"",  // sortOrder
		"",  // knowledgeType
		&enabled,
	)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to list chunks for knowledge ID '%s': %v", input.KnowledgeID, err),
		}, err
	}

	var summaryContent, columnContent string
	for _, chunk := range chunks {
		if chunk.ChunkType == types.ChunkTypeTableSummary {
			summaryContent = chunk.Content
		} else if chunk.ChunkType == types.ChunkTypeTableColumn {
			columnContent = chunk.Content
		}
	}

	if summaryContent == "" || columnContent == "" {
		// This is a missing tool capability, not proof that the source is empty.
		// Return it as a structured failure so the executor retains the reason
		// and navigation instead of replacing it with a generic Go error.
		message := "该文档未提供传统 DuckDB 表结构，本次没有取得表格数据；这不代表原文件没有数据。请使用当前提供的结构化来源目录核对数据集，或用 read_document 读取文档；不要对同一文档重复调用 data_schema。"
		data := map[string]interface{}{
			"kind": "tool_capability", "status": "unavailable", "reason_code": "table_schema_unavailable",
			"knowledge_id": input.KnowledgeID, "retryable": false, "notice": message,
		}
		encoded, _ := json.Marshal(data)
		return &types.ToolResult{Success: false, Error: message, Output: string(encoded), Data: data}, nil
	}

	output := fmt.Sprintf(
		"%s\n\n%s\n\nQuery this document with data_analysis as the table %q.",
		summaryContent, columnContent, DataAnalysisTableName,
	)

	return &types.ToolResult{
		Success: true,
		Output:  output,
		Data: map[string]interface{}{
			"summary": summaryContent,
			"columns": columnContent,
		},
	}, nil
}
