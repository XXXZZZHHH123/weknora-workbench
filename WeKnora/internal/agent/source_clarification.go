package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Take the same finishing-boundary steer snapshot as the normal loop. Pending
// or unobservable corrections fall through to its persistence/drain machinery.
func (e *AgentEngine) hasPendingClarificationSteer(ctx context.Context, sessionID, messageID string) bool {
	if e.steerSink == nil {
		return false
	}
	queued, _, err := e.steerSink.PollSteer(ctx, sessionID, messageID, 0)
	return err != nil || len(queued) != 0
}

type literalSourceField struct {
	DatasetID  string            `json:"dataset_id"`
	SeriesID   string            `json:"series_id"`
	File       string            `json:"source_file"`
	Sheet      string            `json:"sheet"`
	Metric     string            `json:"metric"`
	Unit       string            `json:"unit"`
	Dimensions map[string]string `json:"dimensions"`
	Context    struct {
		Column  string `json:"source_column"`
		Formula struct {
			Cell       string `json:"cell"`
			Formula    string `json:"formula"`
			References []struct {
				Sheet     string `json:"sheet"`
				Reference string `json:"reference"`
				Fields    []struct {
					Column string `json:"column"`
					Metric string `json:"metric"`
				} `json:"fields"`
			} `json:"references"`
		} `json:"formula_source"`
	} `json:"source_context"`
}

// A bare literal name carries no requested computation. Complete, authorized
// catalogue facts suffice to ask which source the user wants; querying each
// ambiguous column and auditing invented statistics adds no useful evidence.
// Do not use this for follow-ups, partial scans, aliases or scoped questions.
func literalSourceClarification(query string, steps []types.AgentStep) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	for _, step := range steps {
		if step.Iteration != -1 {
			continue
		}
		for _, call := range step.ToolCalls {
			if call.Name != "structured_data" || call.Args["operation"] != "list_datasets" || call.Result == nil || !call.Result.Success {
				continue
			}
			var catalog struct {
				Fields    []literalSourceField `json:"literal_fields"`
				Count     int                  `json:"literal_field_count"`
				Truncated *bool                `json:"literal_fields_truncated"`
			}
			if json.Unmarshal([]byte(call.Result.Output), &catalog) != nil || catalog.Truncated == nil || *catalog.Truncated || catalog.Count != len(catalog.Fields) || len(catalog.Fields) < 2 || len(catalog.Fields) > 16 {
				continue
			}
			seen := map[string]bool{}
			valid := true
			for _, field := range catalog.Fields {
				key := field.DatasetID + "/" + field.SeriesID
				if field.Metric != query || field.DatasetID == "" || field.SeriesID == "" || field.File == "" || field.Sheet == "" || field.Context.Column == "" || seen[key] {
					valid = false
					break
				}
				seen[key] = true
			}
			if !valid {
				continue
			}
			var out strings.Builder
			fmt.Fprintf(&out, "当前已导入的可用数据中，“%s”对应多个原表字段。请先选来源口径，避免把同名列混在一起。\n\n", sourceTableText(query))
			out.WriteString("| 文件 / 工作表 | 原表列 | 单位 | 原表维度 | 原件公式样本及引用字段 |\n|---|---|---|---|---|\n")
			for _, field := range catalog.Fields {
				unit := field.Unit
				if unit == "" {
					unit = "未注明"
				}
				formula := "未取得公式样本"
				if field.Context.Formula.Formula != "" {
					formula = field.Context.Formula.Cell + ": " + field.Context.Formula.Formula
					for _, ref := range field.Context.Formula.References {
						for _, parent := range ref.Fields {
							if parent.Metric != "" {
								formula += "；引用“" + ref.Sheet + "”" + parent.Column + "列“" + parent.Metric + "”"
							}
						}
					}
				}
				column := field.Context.Column
				keys := []string{}
				for key, value := range field.Dimensions {
					if key != "原表列" && value != "" {
						keys = append(keys, key)
					}
				}
				sort.Strings(keys)
				dimensions := []string{}
				for _, key := range keys {
					label := key
					value := field.Dimensions[key]
					if key == "block" {
						label = "区块"
						value = strings.TrimPrefix(value, "mapped_")
					}
					dimensions = append(dimensions, label+": "+value)
				}
				dimensionText := strings.Join(dimensions, "；")
				if dimensionText == "" {
					dimensionText = "未注明"
				}
				fmt.Fprintf(&out, "| %s / %s | %s | %s | %s | %s |\n", sourceTableText(field.File), sourceTableText(field.Sheet), sourceTableText(column), sourceTableText(unit), sourceTableText(dimensionText), sourceTableText(formula))
			}
			out.WriteString("\n公式样本只说明所列单元格的依赖，不据此推断整列的业务含义或自然日、交易日周期。以上为已导入字段，未导入区域仍可在原文件中核对。\n\n请指定文件、工作表和原表列，并说明要查看的日期或结果。")
			return out.String()
		}
	}
	return ""
}

func sourceTableText(text string) string {
	text = html.EscapeString(text)
	return strings.NewReplacer("|", "&#124;", "\n", " ", "\r", " ", "`", "&#96;", "[", "&#91;", "]", "&#93;", "*", "&#42;", "_", "&#95;").Replace(text)
}
