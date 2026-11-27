package tools

import (
	"regexp"
	"slices"
	"strings"
)

var documentTableSeparator = regexp.MustCompile(`^:?-{3,}:?$`)

type DocumentTableGrid struct {
	Rows          [][]string
	RawRows       []string
	Context       string
	ParseComplete bool
	ParseWarning  string
	firstLine     int
}

func ParseMarkdownDocumentTableGrids(content string) []DocumentTableGrid {
	lines := strings.Split(content, "\n")
	tables := []DocumentTableGrid{}
	for index := 0; index+1 < len(lines); index++ {
		header, ok := DocumentTableCells(lines[index])
		separator, separatorOK := DocumentTableCells(lines[index+1])
		if !ok || !separatorOK || len(header) != len(separator) {
			continue
		}
		valid := true
		for _, cell := range separator {
			if !documentTableSeparator.MatchString(cell) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		prefix := strings.Join(lines[max(0, index-4):index], "\n")
		if len(prefix) > 2048 {
			prefix = ""
		} // Do not cut UTF-8 or use remote text as a unit header.
		table := DocumentTableGrid{firstLine: index, Rows: [][]string{header}, RawRows: []string{lines[index]}, ParseComplete: true}
		cursor := index + 2
		for ; cursor < len(lines); cursor++ {
			cells, rowOK := DocumentTableCells(lines[cursor])
			if !rowOK || len(cells) != len(header) {
				if strings.HasPrefix(strings.TrimSpace(lines[cursor]), "|") {
					table.ParseComplete = false
					table.ParseWarning = "原表遇到格式或列数与表头不一致的管道行；仅解析此前连续有效行，该行及后续内容未进入此表，不能宣称原表完整覆盖。请对照该实际分块原文核对。"
				}
				break
			}
			table.Rows = append(table.Rows, cells)
			table.RawRows = append(table.RawRows, lines[cursor])
		}
		// Include the preceding source caption and the first three table rows.
		// Model-supplied units must occur in this actual local header context.
		table.Context = prefix + "\n" + strings.Join(table.RawRows[:min(3, len(table.RawRows))], "\n")
		tables = append(tables, table)
		index = cursor - 1
	}
	return tables
}

func DocumentTableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if len(line) < 3 || !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	line = line[1 : len(line)-1]
	cells := []string{}
	var cell strings.Builder
	escaped := false
	for _, char := range line {
		if escaped {
			// Only table delimiters and literal backslashes are unescaped
			// here. Dropping a backslash before a digit would invent a new
			// source value (e.g. 12\34 becoming 1234).
			if char != '|' && char != '\\' {
				cell.WriteByte('\\')
			}
			cell.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteRune(char)
	}
	if escaped {
		cell.WriteByte('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells, len(cells) >= 2
}

// ParseDocumentTableGrids preserves the original grid order for a single body.
// TSV is the historical representation of tables transcribed from images.
func ParseDocumentTableGrids(body string) []DocumentTableGrid {
	if !strings.ContainsAny(body, "|\t") {
		return nil
	}
	grids := ParseMarkdownDocumentTableGrids(body)
	lines := strings.Split(body, "\n")
	for start := 0; start < len(lines); {
		header, ok := documentTSVCells(lines[start])
		if !ok {
			start++
			continue
		}
		grid := DocumentTableGrid{Rows: [][]string{header}, RawRows: []string{lines[start]}, ParseComplete: true, firstLine: start}
		end := start + 1
		for ; end < len(lines); end++ {
			row, ok := documentTSVCells(lines[end])
			if !ok || slices.Equal(header, row) {
				break
			}
			grid.ParseComplete = grid.ParseComplete && len(row) == len(header)
			grid.Rows = append(grid.Rows, row)
			grid.RawRows = append(grid.RawRows, lines[end])
		}
		if len(grid.Rows) > 1 {
			grids = append(grids, grid)
		}
		start = end
	}
	slices.SortStableFunc(grids, func(a, b DocumentTableGrid) int { return a.firstLine - b.firstLine })
	return grids
}

func documentTSVCells(line string) ([]string, bool) {
	if !strings.Contains(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "|") || strings.TrimSpace(line) == "" {
		return nil, false
	}
	cells := strings.Split(line, "\t")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells, true
}
