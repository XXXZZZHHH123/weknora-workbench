package agent

import (
	"fmt"
	"slices"
	"strings"
)

// A workbook inspection can supply one reported operand without a series
// query. The catalog identifies the field. The workbook supplies its header,
// row date and numeric value from the same validated version. Keep it as an
// independently reported reading without treating the comparison as complete.
func inspectedComparisonReading(target taskCoverageTarget, record coverageRecord, attempts []coverageAttempt, draft string) coverageEvidence {
	_, to, _ := coverageTargetDateRange(target)
	if !coverageAnalysisDay(to) {
		return coverageEvidence{}
	}
	var candidates, attributed []coverageEvidence
	for _, field := range record.Evidence {
		if field.Kind != "directory" || field.Operation != "list_series" || field.Sheet != target.Table ||
			field.Metric == "" || field.Column == "" {
			continue
		}
		selected, matches := coveragePrimaryField(target, record, field)
		if selected && !matches {
			continue
		}
		if field.SnapshotHash == "" || !coverageCurrentEvidence(record, field) {
			continue
		}
		searchedField := target.Metric != "" && slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
			return attempt.Success && attempt.ToolCallID == field.ToolCallID && sourceNavigationString(attempt.Args, "search") == target.Metric
		})
		cells := slices.DeleteFunc(slices.Clone(record.Evidence), func(item coverageEvidence) bool {
			return item.Operation != "inspect_source" || item.Kind != "cell" || !item.Bound || item.DatasetID != field.DatasetID ||
				item.KnowledgeBaseID != field.KnowledgeBaseID || item.Sheet != field.Sheet || item.SnapshotHash != field.SnapshotHash
		})
		headerCells := coverageObject(field.Raw)["header_cells"]
		if !slices.ContainsFunc(cells, func(cell coverageEvidence) bool {
			raw, ok := cell.Raw.(string)
			return ok && raw == field.Metric && cell.Column == field.Column && coverageContainsString(headerCells, cell.Cell)
		}) {
			continue
		}
		dateColumn := coverageFieldDateColumn(field)
		for _, date := range cells {
			rawDate, _ := date.Raw.(string)
			column, row := coverageCellPosition(date.Cell)
			if !coverageOriginalDate(rawDate, to) || column == field.Column || row == "" {
				continue
			}
			if dateColumn != "" && column != dateColumn || dateColumn == "" && slices.ContainsFunc(cells, func(cell coverageEvidence) bool {
				otherCol, otherRow := coverageCellPosition(cell.Cell)
				if cell.EvidenceID != date.EvidenceID || otherCol == column || otherRow != row {
					return false
				}
				raw, _ := cell.Raw.(string)
				day, _, _ := strings.Cut(raw, "T")
				return coverageOriginalDate(raw, day)
			}) {
				continue
			}
			for _, value := range cells {
				if value.Cell != field.Column+row {
					continue
				}
				item, ok := coveragePinInspectedFieldObservation(field, date, value, to)
				if !ok {
					continue
				}
				choices := &candidates
				if matches || field.Metric == target.Metric || draftNamesComparisonCell(draft, item) {
					choices = &attributed
				} else if !searchedField {
					continue
				}
				if !slices.ContainsFunc(*choices, func(prior coverageEvidence) bool {
					return prior.SeriesID == item.SeriesID && prior.DateCell == item.DateCell && coverageSameOriginalCell(prior, item) && reviewJSONValuesEqual(prior.Raw, item.Raw)
				}) {
					*choices = append(*choices, item)
				}
			}
		}
	}
	if len(attributed) != 0 {
		candidates = attributed
	}
	if len(candidates) != 1 {
		return coverageEvidence{}
	}
	return candidates[0]
}

func terminalDirectoryWorkbook(record coverageRecord, field coverageEvidence) (snapshot, title string) {
	for _, item := range record.Evidence {
		if item.Kind != "directory" || item.Operation != "list_datasets" || item.DatasetID != field.DatasetID ||
			item.KnowledgeBaseID != field.KnowledgeBaseID || item.SourceTitle == "" || !validFigureDigest(item.SnapshotHash) {
			continue
		}
		if snapshot != "" && snapshot != item.SnapshotHash {
			return "", ""
		}
		snapshot, title = item.SnapshotHash, item.SourceTitle
	}
	return snapshot, title
}

func (d *terminalCoverageDelivery) nameUnresolvedComparisonSide() {
	var sources []string
	for _, read := range d.readReceipts {
		if read.SourceTitle != "" && !slices.Contains(sources, read.SourceTitle) && !slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
			return fact.Role == "scoped_value" && comparisonPhysicalSource(fact.Evidence) == comparisonPhysicalSource(read)
		}) {
			sources = append(sources, read.SourceTitle)
		}
	}
	if len(sources) != 1 || !slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
		_, hasNumber := coverageComparisonNumber(fact.Evidence)
		return fact.Role == "scoped_value" && hasNumber
	}) {
		return
	}
	prior := d.comparisonGap
	d.comparisonGap = fmt.Sprintf(d.text("本轮未从 %s 的片段中读到可与上面读数核对的数，因此无法判断两处是否一致。",
		"The passages read from %s did not supply a matching value, so it is unclear whether the sources agree."), terminalEvidenceText(sources[0]))
	for i, note := range d.missing {
		if note == prior {
			d.missing[i] = d.comparisonGap
		}
	}
}

func (d terminalCoverageDelivery) unreadFindings() string {
	var lines []string
	add := func(line string) {
		if !slices.Contains(lines, line) {
			lines = append(lines, line)
		}
	}
	for _, item := range d.record.Evidence {
		if item.Kind != "directory" || item.Operation != "list_series" || item.Metric == "" || item.Sheet == "" ||
			!slices.ContainsFunc(d.requested, func(target taskCoverageTarget) bool {
				selected, matches := coveragePrimaryField(target, d.record, item)
				return matches || !selected && target.Table == item.Sheet && (target.Metric == "" || target.Metric == item.Metric)
			}) {
			continue
		}
		if item.SourceTitle == "" {
			_, item.SourceTitle = terminalDirectoryWorkbook(d.record, item)
		}
		from := coverageDay(sourceNavigationString(coverageObject(item.Raw), "first_date"))
		to := coverageDay(item.LastObservedDate)
		label := terminalCoverageLabel(item)
		line := fmt.Sprintf(d.text("已在目录中定位 %s。", "The catalog lists %s."), label)
		if from != "" && to != "" && from <= to {
			line = fmt.Sprintf(d.text("%s 的目录列出的日期范围为 %s。", "The catalog lists %[2]s for %[1]s."), label, terminalCoveragePeriod(from, to, ""))
		}
		add(line)
	}
	if len(lines) == 0 {
		for _, item := range d.record.Evidence {
			if item.SourceTitle != "" && item.Kind == "directory" {
				add(fmt.Sprintf(d.text("本轮查阅了 %s 的来源目录。", "This turn checked the source listing for %s."), terminalEvidenceText(item.SourceTitle)))
			}
		}
	}
	if len(lines) > 3 {
		lines = lines[:3]
	}
	for _, item := range d.readReceipts {
		location := terminalEvidenceText(item.SourceTitle)
		if location == "" {
			continue
		}
		if item.Page > 0 {
			add(fmt.Sprintf(d.text("本轮读取了 %s 第 %d 页。", "This turn read page %[2]d of %[1]s."), location, item.Page))
		} else {
			add(fmt.Sprintf(d.text("本轮读取了 %s 返回的正文片段。", "This turn read the passages returned from %s."), location))
		}
	}
	for _, note := range d.notes {
		add(note)
	}
	if d.attemptCount == 0 {
		add(d.text("本轮没有执行可核验的来源查询或读取。", "This turn did not execute a verifiable source query or read."))
	}
	scopeStart := len(lines)
	if d.comparisonGap != "" {
		add(d.comparisonGap)
	} else {
		for _, target := range d.requested {
			from, to, _ := coverageTargetDateRange(target)
			from, to = coverageDay(from), coverageDay(to)
			period := terminalCoveragePeriod(from, to, "")
			if from != "" && to == "" {
				period = fmt.Sprintf(d.text("自 %s 起", "from %s onward"), from)
			} else if from == "" && to != "" {
				period = fmt.Sprintf(d.text("截至 %s", "through %s"), to)
			}
			field := firstNonEmptyAgent(target.Metric, target.Table)
			if taskCoverageSourceHandle(field) {
				field = ""
			}
			scope := strings.TrimSpace(terminalEvidenceText(field) + " " + period)
			if scope == "" {
				continue
			}
			line := fmt.Sprintf(d.text("本轮未读到 %s 对应的原始记录。", "No original records for %s were read in this turn."), scope)
			if target.Delivery["aggregation"] == "min_max" {
				line = fmt.Sprintf(d.text("本轮未读到 %s 的原始记录，最高值、最低值及出现日期尚无法确定。", "This turn did not read the original records for %s. The high, low and their dates remain undetermined."), scope)
			}
			add(line)
		}
	}
	if len(lines) == scopeStart {
		if d.subject != "" {
			add(fmt.Sprintf(d.text("关于“%s”，本轮未读到能核实所问结果的原始记录。", "The original records needed to verify “%s” were not read in this turn."), d.subject))
		} else {
			add(d.text("本轮尚未读到可核实的原始记录。", "No verifiable original records were read in this turn."))
		}
	}
	return strings.Join(lines, "\n\n")
}

func (d terminalCoverageDelivery) changedSourceFindings() string {
	result := d.finalReadStatus(terminalSourceChanged)
	if d.attemptCount == 0 {
		return result + " " + d.text("当前授权范围内没有可重新核验的原始记录。", "The current access scope contains no saved original records that could be rechecked.")
	}
	return result + " " + fmt.Sprintf(d.text("重新核对了 %d 条仍可检查的来源尝试，没有取得可公开的原始读数。",
		"Rechecking %d available source attempts produced no original reading that could be shared."), d.attemptCount)
}
