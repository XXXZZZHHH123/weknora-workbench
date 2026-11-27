package agent

import (
	"fmt"
	"html"
	"math/big"
	"regexp"
	"slices"
	"strings"
)

// A fixed decimal format from a current original cell can be rendered with
// exact rational arithmetic. Other Excel formats may encode units or change
// magnitudes. They stay outside this presentation rule.
var comparisonDecimalFormat = regexp.MustCompile(`^0(?:\.(0{1,12}))?$`)

func comparisonOriginalDisplay(record coverageRecord, value coverageEvidence, amount *big.Rat) string {
	format := value.OriginalNumberFormat
	for _, item := range record.Evidence {
		if item.Operation != "inspect_source" || item.Kind != "cell" || !item.Bound || !coverageCurrentEvidence(record, item) ||
			item.SourceID != value.SourceID || item.KnowledgeBaseID != value.KnowledgeBaseID ||
			item.SnapshotHash != value.SnapshotHash || item.SnapshotHash == "" ||
			item.Sheet != value.Sheet || item.Cell != value.Cell {
			continue
		}
		inspected, ok := coverageComparisonNumber(item)
		if !ok || inspected.Cmp(amount) != 0 {
			continue
		}
		if item.OriginalNumberFormat == "" {
			continue
		}
		if format != "" && format != item.OriginalNumberFormat {
			return ""
		}
		format = item.OriginalNumberFormat
	}
	match := comparisonDecimalFormat.FindStringSubmatch(format)
	if match == nil {
		return ""
	}
	return amount.FloatString(len(match[1]))
}

func comparisonValueText(value coverageEvidence, amount *big.Rat, preserveExact bool) (string, bool) {
	literal := value.ReportedValue
	if value.Kind != "body" {
		literal, _ = coverageDraftRaw(value)
	}
	_, fractional, _ := strings.Cut(literal, ".")
	if preserveExact || strings.ContainsAny(literal, "eE") || len(fractional) <= 4 {
		return terminalEvidenceText(literal), false
	}
	return amount.FloatString(2), true
}

func (d *terminalCoverageDelivery) addVerifiedComparison(a, b coverageEvidence, left, right *big.Rat) {
	if comparisonPhysicalSource(a) == comparisonPhysicalSource(b) {
		return
	}
	// Preserve disclosed literals. A long cache literal can be presented
	// approximately to two decimal places when that still distinguishes
	// the readings. All relations use the exact original operands.
	preserveExact := left.Cmp(right) != 0 && left.FloatString(2) == right.FloatString(2)
	if d.comparisonValues == nil {
		d.comparisonValues = make(map[string]string)
	}
	for _, side := range []struct {
		evidence coverageEvidence
		amount   *big.Rat
	}{{a, left}, {b, right}} {
		item := side.evidence
		if item.Kind == "body" {
			d.comparisonValues[terminalCoverageFactKey(item)] = terminalEvidenceText(item.ReportedValue)
			continue
		}
		d.comparisonValues[terminalCoverageFactKey(item)] = d.verifiedComparisonValue(item, side.amount, preserveExact)
	}
	var conclusion string
	equal := left.Cmp(right) == 0
	if equal {
		conclusion = d.text("两处来源给出的数字相同。", "The two sources report the same number.")
	} else {
		higher, lower := a, b
		if left.Cmp(right) < 0 {
			higher, lower = b, a
		}
		winner, other := terminalEvidenceText(higher.SourceTitle), terminalEvidenceText(lower.SourceTitle)
		if winner == other {
			winner, other = terminalEvidenceText(d.fallbackSourceTitle(higher)), terminalEvidenceText(d.fallbackSourceTitle(lower))
		}
		difference := new(big.Rat).Abs(new(big.Rat).Sub(left, right))
		formatted := difference.FloatString(2)
		rounded, _ := new(big.Rat).SetString(formatted)
		conclusion = fmt.Sprintf(d.text("两处来源给出的数字不同，%s 高于 %s。", "The two sources report different numbers. %s is higher than %s."), winner, other)
		if rounded.Sign() > 0 {
			quantity := d.text("原报数字相差", "The two reported numbers differ by")
			if rounded.Cmp(difference) != 0 {
				quantity = d.text("原报数字相差约", "The two reported numbers differ by approximately")
			}
			conclusion += fmt.Sprintf(d.text("%s %s。", " %s %s."), quantity, formatted)
		}
	}
	switch comparisonUnitStatus(a, b) {
	case "incompatible":
		if equal {
			conclusion += d.text("来源单位不同，无法据此确认量值相同。", " The source units differ, so that does not establish equal quantities.")
		} else {
			conclusion += d.text("来源单位不同，数字之差不能作为实物量之差。", " The source units differ, so the numeric difference cannot be used as a difference in quantity.")
		}
	case "unknown":
		if equal {
			conclusion += d.text("双方单位尚未核实为一致，无法据此确认量值相同。", " A common unit remains unverified, so that does not establish equal quantities.")
		} else {
			conclusion += d.text("双方单位尚未核实为一致，数字之差仅反映各自披露的读数。", " A common unit remains unverified. The difference describes the reported numbers.")
		}
	}
	if !slices.Contains(d.conclusions, conclusion) {
		d.conclusions = append(d.conclusions, conclusion)
	}
}

func (d terminalCoverageDelivery) verifiedComparisonValue(item coverageEvidence, amount *big.Rat, preserveExact bool) string {
	view, approximate := comparisonValueText(item, amount, preserveExact)
	if display := comparisonOriginalDisplay(d.record, item, amount); display != "" {
		// The workbook format is a separate observation from its
		// underlying numeric cache.
		if display != view {
			if approximate {
				display += fmt.Sprintf(d.text("（缓存值约 %s）", " (cached value approximately %s)"), view)
			} else {
				display += fmt.Sprintf(d.text("（底层数值 %s）", " (underlying value %s)"), view)
			}
		}
		view = display
	} else if approximate {
		view = d.text("约 ", "approximately ") + view
	}
	return view
}

func (d terminalCoverageDelivery) appendComparisonCitation(sentence string, item coverageEvidence) string {
	if d.comparisonValues[terminalCoverageFactKey(item)] == "" {
		return sentence
	}
	if item.Kind == "body" && item.ChunkID != "" && item.KnowledgeBaseID != "" {
		return sentence + fmt.Sprintf(`<kb chunk_id="%s" kb_id="%s"/>`, html.EscapeString(item.ChunkID), html.EscapeString(item.KnowledgeBaseID))
	}
	if item.CitationID != "" {
		return sentence + fmt.Sprintf(`<evidence evidence_id="%s"/>`, html.EscapeString(item.CitationID))
	}
	return sentence
}

// Catalog receipts establish which sources were located. They establish
// neither an original value nor an absence in the underlying source.
func (d terminalCoverageDelivery) verifiedSourceStatus() string {
	var documentCatalog bool
	workbooks := make(map[[2]string]string)
	fields := make(map[[3]string]coverageEvidence)
	var scope string
	var comparing bool
	for _, target := range d.requested {
		comparing = comparing || target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions"
		if scope != "" || target.Metric == "" && target.Table == "" {
			continue
		}
		from, to, _ := coverageTargetDateRange(target)
		period := terminalCoveragePeriod(from, to, sourceNavigationString(target.Period, "scope"))
		field := strings.TrimSpace(firstNonEmptyAgent(target.Metric, target.Table) + " " + period)
		scope = terminalEvidenceText(field)
	}
	for _, item := range d.record.Evidence {
		if !item.Bound || item.Kind != "directory" {
			continue
		}
		if item.SourceDomain == "knowledge" {
			documentCatalog = true
		}
		if item.SourceDomain != "structured" {
			continue
		}
		if item.Operation == "list_datasets" && item.SourceTitle != "" {
			workbooks[comparisonPhysicalSource(item)] = terminalEvidenceText(item.SourceTitle)
		}
		if item.Operation != "list_series" || item.SeriesID == "" || item.Sheet == "" || item.Metric == "" {
			continue
		}
		if len(d.requested) > 0 && !slices.ContainsFunc(d.requested, func(target taskCoverageTarget) bool {
			return target.Metric == item.Metric && (target.Table == "" || target.Table == item.Sheet)
		}) {
			continue
		}
		fields[[3]string{item.KnowledgeBaseID, item.SourceID, item.SeriesID}] = item
	}
	var concrete string
	if len(fields) == 1 {
		for _, field := range fields {
			field.SourceTitle = d.fallbackSourceTitle(field)
			concrete = fmt.Sprintf(d.text("字段目录列出了 %s。", "The field catalog identified %s."), terminalCoverageLabel(field))
			if date := coverageDay(field.LastValidDate); date != "" {
				concrete += fmt.Sprintf(d.text("目录登记的最后可用日期为 %s。", " The catalog records %s as its last available date."), date)
			}
		}
	} else if len(workbooks) == 1 {
		for _, workbook := range workbooks {
			concrete = fmt.Sprintf(d.text("数据目录定位到 %s。", "The data catalog located %s."), workbook)
		}
	}
	if len(d.readReceipts) == 1 {
		read := d.readReceipts[0]
		if read.Page > 0 && read.SourceTitle != "" {
			label := terminalEvidenceText(read.SourceTitle)
			concrete = strings.TrimSpace(concrete + " " + fmt.Sprintf(d.text("本次读取了 %s 第 %d 页。", "This run read %s page %d."), label, read.Page))
		}
	}
	if concrete == "" && (!documentCatalog || scope == "") {
		return ""
	}
	var boundary string
	switch {
	case comparing && scope != "":
		boundary = fmt.Sprintf(d.text("现有证据无法判断 %s 在两处来源的读数是否一致。", "The available evidence cannot establish whether the two sources agree on %s."), scope)
	case scope != "":
		boundary = fmt.Sprintf(d.text("现有来源回执无法确定 %s 的读数。", "The available source receipts do not establish a reading for %s."), scope)
	case d.subject != "":
		boundary = fmt.Sprintf(d.text("关于“%s”，上述回执未核实所问的原值。", "The receipts above do not establish an original value for “%s”."), d.subject)
	default:
		return ""
	}
	if documentCatalog && concrete == "" {
		concrete = d.text("文档检索返回了定位信息，尚无对应的原表读数。", "The search returned document locations, which supply no reading from the original table.")
	}
	if scope == "" {
		return strings.TrimSpace(concrete + " " + boundary)
	}
	return strings.TrimSpace(boundary + " " + concrete)
}
