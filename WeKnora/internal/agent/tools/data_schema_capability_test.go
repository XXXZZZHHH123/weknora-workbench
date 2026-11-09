package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestDataSchemaUnavailablePreservesCapabilityReason(t *testing.T) {
	for _, chunks := range [][]*types.Chunk{
		nil,
		{{ChunkType: types.ChunkTypeTableSummary, Content: "summary"}},
		{{ChunkType: types.ChunkTypeTableColumn, Content: "columns"}},
	} {
		doc := &types.Knowledge{ID: "doc-1", TenantID: 7, KnowledgeBaseID: "kb-1"}
		repo := &readDocChunkRepo{ordered: chunks}
		tool := NewDataSchemaTool(&readDocKnowledgeService{docs: map[string]*types.Knowledge{doc.ID: doc}}, repo).
			WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}})
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_id":"doc-1"}`))
		if err != nil || result == nil || result.Success || result.Data["reason_code"] != "table_schema_unavailable" || result.Data["retryable"] != false {
			t.Fatalf("missing capability lost its structured reason: result=%+v err=%v", result, err)
		}
		var payload map[string]any
		if json.Unmarshal([]byte(result.Output), &payload) != nil || payload["knowledge_id"] != doc.ID || !strings.Contains(result.Error, "不代表原文件没有数据") {
			t.Fatalf("missing schema must preserve source identity and next action: %+v", result)
		}
	}
}

func TestDataSchemaCapabilityDoesNotMaskRepositoryOrPermissionFailure(t *testing.T) {
	doc := &types.Knowledge{ID: "doc-1", TenantID: 7, KnowledgeBaseID: "kb-1"}
	for _, denied := range []bool{true, false} {
		repo := &readDocChunkRepo{listErr: errors.New("repository unavailable")}
		kb := "kb-1"
		if denied {
			kb = "other-kb"
		}
		tool := NewDataSchemaTool(&readDocKnowledgeService{docs: map[string]*types.Knowledge{doc.ID: doc}}, repo).
			WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb, TenantID: 7}})
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_id":"doc-1"}`))
		if err == nil || result.Success || result.Data["reason_code"] == "table_schema_unavailable" {
			t.Fatalf("actual failure must stay distinct from missing schema: result=%+v err=%v", result, err)
		}
	}
}

func TestDataSchemaAvailableReturnsOriginalSchema(t *testing.T) {
	doc := &types.Knowledge{ID: "doc-1", TenantID: 7, KnowledgeBaseID: "kb-1"}
	repo := &readDocChunkRepo{ordered: []*types.Chunk{
		{ChunkType: types.ChunkTypeTableSummary, Content: "original summary"},
		{ChunkType: types.ChunkTypeTableColumn, Content: "amount DECIMAL(12, 2)"},
	}}
	tool := NewDataSchemaTool(&readDocKnowledgeService{docs: map[string]*types.Knowledge{doc.ID: doc}}, repo)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"knowledge_id":"doc-1"}`))
	if err != nil || !result.Success || result.Data["summary"] != "original summary" || result.Data["columns"] != "amount DECIMAL(12, 2)" {
		t.Fatalf("available schema changed: result=%+v err=%v", result, err)
	}
}
