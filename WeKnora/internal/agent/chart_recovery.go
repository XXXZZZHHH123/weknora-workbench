package agent

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
)

const chartRecoveryCallBudget = 256

// Only a source already selected from the current authenticated catalog may
// receive this diagnostic. It does not execute a read, choose axes, or repair
// SQL values. Failed tool Data is not model-visible unless encoded in Output.
func (t *renderChartTool) sourceFailureDiagnostics(message string, source chart.Table, steps []types.AgentStep) *types.ToolResult {
	diagnostic := map[string]any{
		"source_id":    source.ID,
		"matrix_shape": map[string]int{"rows": len(source.Rows), "columns": len(source.Columns)},
		"mapping_coordinates": map[string]string{
			"rows":    "x_index/y_indexes取原行；value_indexes取原列，排除label_index指定的标签列；unit_index仅复制原列已声明单位。",
			"columns": "x_index/y_indexes取原列；value_indexes取原行，包括真实第0行；轴名与单位复制所选原列元数据，label_index/unit_index不改变观测行。",
		},
		"notice": "映射只复制当前结果，不能修复SQL错误配对。仅明确的语义不匹配才考虑用raw_source_reads重取原单元格；菜单不确认业务维度、不自动执行或生成SQL。核验临时未完成时，保持同一来源、mapping及全部绘图参数重试。",
	}
	rootID := source.ID
	if mapped, ok := t.mapped[source.ID]; ok {
		rootID = mapped.ParentID
	}
	if reads := chartRawSourceReads(steps, rootID); len(reads) > 0 {
		diagnostic["raw_source_reads"] = reads
	}
	data := map[string]any{"kind": "data_chart", "status": "error", "error": message, "source_diagnostics": diagnostic}
	body, _ := json.Marshal(data)
	return &types.ToolResult{Success: false, Error: message, Data: data, Output: string(body)}
}

// Reconstruct the exact current root handle from an actual successful PDF
// execution. Never infer identity by walking arbitrary nested provenance or
// from the requested ID, old turns, a rejected result, or another PDF table.
func chartRawSourceReads(steps []types.AgentStep, rootID string) []map[string]any {
	checked := 0
	for si := len(steps) - 1; si >= 0; si-- {
		for ci := len(steps[si].ToolCalls) - 1; ci >= 0; ci-- {
			checked++
			if checked > chartRecoveryCallBudget {
				return nil
			}
			call := steps[si].ToolCalls[ci]
			if call.Result == nil || !call.Result.Success {
				continue
			}
			read := isStructuredPDFSourceRead(call)
			query := call.ExecutionName() == "structured_query_pdf_table" || call.ExecutionName() == "structured_data" && call.ExecutionArgs()["operation"] == "query_pdf_table"
			if !read && !query {
				continue
			}
			if query {
				table, err := checkedStructuredChartTable(call)
				if err != nil || table.Seal(call.ID+"/structured") != nil || table.ID != rootID {
					continue
				}
			} else {
				tables := pdfChartInputSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
				if len(tables) != 1 || tables[0].ID != rootID {
					continue
				}
			}
			encoded, err := json.Marshal(call.Result.Data)
			if err != nil || len(encoded) > chart.MaxSourceBytes {
				return nil
			}
			var data struct {
				Kind       string `json:"kind"`
				Status     string `json:"status"`
				SourceKind string `json:"source_kind"`
				TableID    string `json:"table_id"`
				Source     struct {
					KnowledgeBaseID string `json:"knowledge_base_id"`
					SHA256          string `json:"sha256"`
				} `json:"source"`
				SourceContext struct {
					KnowledgeBaseID string `json:"knowledge_base_id"`
				} `json:"source_context"`
			}
			if json.Unmarshal(encoded, &data) != nil || data.Status != "ok" || data.SourceKind != "pdf_table" || query && data.Kind != "structured_analysis" || read && data.Kind != "structured_source_inspection" {
				return nil
			}
			args := call.ExecutionArgs()
			knowledgeBaseID := args["knowledge_base_id"]
			if call.ExecutionName() == "structured_data" {
				args, _ = args["arguments"].(map[string]any)
				// The gateway binds scope in its envelope, never in the
				// operation arguments. Do not accept an inner scope override.
				if _, nestedScope := args["knowledge_base_id"]; nestedScope {
					return nil
				}
			}
			if !chartRecoveryIdentity(data.TableID) || !chartRecoveryIdentity(data.Source.KnowledgeBaseID) || args["table_id"] != data.TableID || knowledgeBaseID != data.Source.KnowledgeBaseID || data.SourceContext.KnowledgeBaseID != "" && data.SourceContext.KnowledgeBaseID != data.Source.KnowledgeBaseID {
				return nil
			}
			sha, err := hex.DecodeString(data.Source.SHA256)
			if err != nil || len(sha) != 32 || !chartRecoveryIdentity(call.ID) {
				return nil
			}
			return []map[string]any{{"tool": "structured_read_pdf_table", "arguments": map[string]string{"knowledge_base_id": data.Source.KnowledgeBaseID, "table_id": data.TableID}, "source_tool_call_id": call.ID, "source_sha256": data.Source.SHA256, "notice": "精确匹配当前来源的成功PDF工具记录；再次读取仍须通过现有权限及版本检查，读取前不得当作已恢复的原始单元格。"}}
		}
	}
	return nil
}

func chartRecoveryIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
