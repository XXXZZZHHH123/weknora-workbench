package modelcontext

import (
	"encoding/json"
	"fmt"
	"strings"
)

const originalFigureOverviewBudget = 8 * 1024

// Place every returned curve's existing roles side by side before the detailed
// source rendering. This is a presentation view, never a new analysis or a
// replacement for the full trajectory, source identity or canonical result.
func originalFigureRoleOverview(evidence map[string]any) string {
	geometry, _ := evidence["geometry"].(map[string]any)
	if evidence["version"] != "source_figure_geometry_v1" || geometry["method"] != "original_emf_trace_geometry_v1" {
		return ""
	}
	series := mapsValue(geometry["series"])
	omitted := func(reason string) string {
		return "关键点概览未展示（" + reason + "）；下方原图详细证据保持完整，不据此推断缺少曲线或观测。\n\n"
	}
	if len(series) == 0 || intValue(geometry, "series_count") != len(series) {
		return omitted("返回曲线信息不完整")
	}
	unit, changeUnit := stringValue(geometry, "unit"), stringValue(geometry, "change_unit")
	if unit == "" {
		unit = "未声明"
	}
	if changeUnit == "" {
		changeUnit = "未声明"
	} else if changeUnit == "percentage_points" {
		changeUnit = "百分点"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "原图关键点概览：本次返回的全部%d条曲线。以下点值及上下界的单位为【%s】；净变动的单位为【%s】，不可混写。\n", len(series), overviewCell(unit), overviewCell(changeUnit))
	b.WriteString("列按角色排列，并非时间先后；先后须看真实横轴位置，原有key_points在下方按横轴顺序排列。区间内的比例从左刻度起算，不能归到右刻度之后；刻度外不外推日期。每条曲线只覆盖其首点至末点，短曲线不能代表完整周期。\n")
	b.WriteString("归组和总体方向须与各曲线的实际位置、关键点先后及覆盖范围一致；某一组的特征不能代替整体，结尾归纳不能与这些点相反。关键点间净变动不证明期间单调，细节及采样仍保留在下方。\n")
	b.WriteString("| 原图图例 | 首点：位置；点值 [下界,上界] | 最高点：位置；点值 [下界,上界] | 最低点：位置；点值 [下界,上界] | 末点：位置；点值 [下界,上界] |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range series {
		points := mapsValue(row["key_points"])
		byRole := map[string]map[string]any{}
		for _, point := range points {
			encoded, _ := json.Marshal(point["roles"])
			var roles []string
			if json.Unmarshal(encoded, &roles) != nil {
				continue
			}
			for _, role := range roles {
				if _, exists := byRole[role]; exists {
					return omitted("关键点角色不唯一")
				}
				byRole[role] = point
			}
		}
		cells := []string{overviewCell(stringValue(row, "legend"))}
		for _, role := range []string{"first", "maximum", "minimum", "last"} {
			point := byRole[role]
			if len(points) == 0 {
				point, _ = row[role].(map[string]any)
			}
			value := overviewNumber(point["approximate_value"])
			position := originalFigurePointPosition(point)
			if value == "" || position == "" {
				return omitted("关键点角色或位置不完整")
			}
			bounds := ""
			if low, high := overviewNumber(point["estimate_low"]), overviewNumber(point["estimate_high"]); low != "" && high != "" {
				bounds = " [" + low + "," + high + "]"
			}
			cells = append(cells, overviewCell(position+"；"+value+" "+unit+bounds))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if b.Len() > originalFigureOverviewBudget {
			return omitted("大小超过概览预算，未截取部分曲线")
		}
	}
	b.WriteString("\n以下为原图完整详细证据：\n")
	if b.Len() > originalFigureOverviewBudget {
		return omitted("大小超过概览预算，未截取部分曲线")
	}
	return b.String()
}

func originalFigurePointPosition(point map[string]any) string {
	if interval, ok := point["x_axis_interval"].(map[string]any); ok {
		left, right, fraction := stringValue(interval, "left_label"), stringValue(interval, "right_label"), overviewNumber(interval["fraction_between_labels"])
		if left != "" && right != "" && fraction != "" {
			return "【" + left + "】→【" + right + "】内，左起比例=" + fraction
		}
	}
	if edge, ok := point["x_axis_edge"].(map[string]any); ok {
		label, side, distance := stringValue(edge, "edge_label"), stringValue(edge, "side"), overviewNumber(edge["distance_in_adjacent_tick_spacings"])
		if label != "" && distance != "" && (side == "before_first" || side == "after_last") {
			direction := "左"
			if side == "after_last" {
				direction = "右"
			}
			return "刻度【" + label + "】" + direction + "侧，距离=" + distance + "个相邻刻度间隔"
		}
	}
	if position, ok := point["axis_position"].(map[string]any); ok {
		encoded, err := json.Marshal(position)
		if err == nil {
			return string(encoded)
		}
	}
	if fraction := overviewNumber(point["x_fraction"]); fraction != "" {
		return "全轴归一位置=" + fraction + "（无可用刻度边界，不推断日期）"
	}
	return ""
}

func overviewNumber(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || (encoded[0] != '-' && (encoded[0] < '0' || encoded[0] > '9')) {
		return ""
	}
	return string(encoded)
}

func overviewCell(value string) string {
	return strings.NewReplacer("|", "\\|", "\r", " ", "\n", " ").Replace(value)
}
