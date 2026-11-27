package tools

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	structuredLastValidArgument     = "include_last_valid"
	structuredLastValidInstructions = " 对点名来源的日期取值，若原问题允许用同源最近有效值说明缺期，选定与原问题同口径的字段后，在同次读取中设置 include_last_valid=true。先核对实际表头、指标、维度和单位，避免把包含其他分项的合计当作单项。区间行和统计仍只覆盖请求窗口。用 end_date 指定端点上界，SQL 的 WHERE 不限定端点。最近有效观测只按同一字段的 coverage.last_valid（SQL 为 inputs[series_id].coverage.last_valid）交付原值、日期和原格；字段目录日期、最后记录、样例行窗及其他字段均不能证明它。before_range 表示窗前；conflict 或 truncated 时说明同日冲突或截断。若明确只允许窗口内观测，设置 include_last_valid=false。"
)

func (d StructuredOperationDescriptor) SupportsLastValid() bool {
	return d.lastValid && structuredresult.LastValidEnabled()
}

func structuredLastValidOperation(operation string) bool {
	return operation == "query_series" || operation == "analyze_table"
}

func structuredAPIKeyScopeDigest(key types.TenantAPIKeyScope) string {
	// The caller supplies only the verified and normalized context scope.
	// Sorting the grants makes equivalent permission sets share a receipt.
	knowledgeBases := slices.Clone(key.KnowledgeBaseIDs)
	capabilities := slices.Clone(key.Capabilities)
	slices.Sort(knowledgeBases)
	slices.Sort(capabilities)
	return structuredDigest([]any{"structured-api-key-scope-v1", key.KeyID, key.ScopeType, key.FullAccess, knowledgeBases, capabilities})
}

func structuredLastValidModelOutput(operation string, arguments map[string]json.RawMessage, data map[string]any, knowledgeBaseID string) ([]byte, bool) {
	if _, unbound := data["last_valid"]; unbound {
		return nil, false
	}
	_, needsContract := data["result_contract"]
	available := 0
	expected := 1
	if operation == "query_series" {
		present, valid, coveragePresent := readStructuredLastValid(data)
		if !valid {
			return nil, false
		}
		_, rangePresent := data["requested_range"]
		needsContract = needsContract || coveragePresent || rangePresent
		if present {
			available++
		}
	} else {
		var ids []string
		_ = json.Unmarshal(arguments["series_ids"], &ids)
		selected := make(map[string]bool, len(ids))
		for _, id := range ids {
			selected[id] = true
		}
		expected = max(1, len(selected))
		inputs, _ := data["inputs"].(map[string]any)
		for id, input := range inputs {
			field, _ := input.(map[string]any)
			present, valid, coveragePresent := readStructuredLastValid(field)
			if !valid || (coveragePresent && !selected[id]) {
				return nil, false
			}
			needsContract = needsContract || coveragePresent
			if present {
				available++
			}
		}
	}
	if needsContract {
		contract, err := structuredresult.ValidatePayload(data)
		if err != nil || contract == nil || (knowledgeBaseID != "" && contract.SourceSnapshot.KnowledgeBaseID != "" && contract.SourceSnapshot.KnowledgeBaseID != knowledgeBaseID) {
			return nil, false
		}
	}
	view := data
	if available != expected && data["kind"] != "structured_source_binding" {
		// Keep the canonical service result and its hash unchanged. This notice
		// is sent only to the model for a requested, missing endpoint.
		view = maps.Clone(data)
		view["last_valid_notice"] = "部分或全部所选字段的最近有效观测本次未取得。只有返回了相应字段的 coverage.last_valid 时，才能按它的状态说明端点。字段范围和示例行没有证明最近有效观测。"
	}
	// Serialize the same decoded values that passed validation, including
	// json.Number. This keeps duplicate wire keys and ignored values out of the
	// model view while retaining the result contract's typed hash.
	output, err := json.Marshal(view)
	return output, err == nil
}

func readStructuredLastValid(field map[string]any) (present, valid, coveragePresent bool) {
	rawCoverage, coveragePresent := field["coverage"]
	if !coveragePresent {
		return false, true, false
	}
	coverage, ok := rawCoverage.(map[string]any)
	if !ok {
		return false, false, true
	}
	endpoint, exists := coverage["last_valid"]
	if !exists {
		return false, true, true
	}
	value, _ := endpoint.(map[string]any)
	switch value["status"] {
	case "observed", "no_valid", "conflict", "truncated", "not_ordered":
		return true, true, true
	default:
		return true, false, true
	}
}
