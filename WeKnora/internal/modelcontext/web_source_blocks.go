package modelcontext

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// webSourceBlock describes source layout, never a semantic interpretation of a
// date, metric or query. Byte positions are internal; the model sees the same
// Unicode character offsets accepted by web_fetch.
type webSourceBlock struct {
	start  int
	parent int
	owner  int
}

type webSourceHeading struct {
	level int
	start int
}

func webMarkdownBlocks(source string) ([]webSourceBlock, bool) {
	data := []byte(source)
	document := goldmark.DefaultParser().Parse(text.NewReader(data))
	blocks := []webSourceBlock{{start: 0, parent: -1, owner: -1}}
	structured := false
	lineStart := func(node ast.Node) int {
		pos := node.Pos()
		if pos < 0 || pos > len(source) {
			return -1
		}
		return strings.LastIndexByte(source[:pos], '\n') + 1
	}
	add := func(start, parent, owner int) {
		if start < 0 || start >= len(source) {
			return
		}
		last := &blocks[len(blocks)-1]
		if start == last.start {
			// A list marker and its heading can share a source line. Keep one
			// physical block, without inventing a parent that points to itself.
			if parent != start {
				last.parent = parent
			}
			last.owner = owner
		} else if start > last.start {
			blocks = append(blocks, webSourceBlock{start: start, parent: parent, owner: owner})
		}
	}
	var visit func(ast.Node, int)
	visit = func(container ast.Node, base int) {
		var headings []webSourceHeading
		current := func() int {
			if len(headings) > 0 {
				return headings[len(headings)-1].start
			}
			return base
		}
		for node := container.FirstChild(); node != nil; node = node.NextSibling() {
			switch node := node.(type) {
			case *ast.Heading:
				structured = true
				start := lineStart(node)
				for len(headings) > 0 && headings[len(headings)-1].level >= node.Level {
					headings = headings[:len(headings)-1]
				}
				add(start, current(), start)
				headings = append(headings, webSourceHeading{level: node.Level, start: start})
			case *ast.List:
				structured = true
				for item := node.FirstChild(); item != nil; item = item.NextSibling() {
					start := lineStart(item)
					add(start, current(), start)
					visit(item, start)
				}
			case *ast.Blockquote:
				start := lineStart(node)
				add(start, current(), start)
				visit(node, start)
			default:
				// Paragraphs, tables and code are kept verbatim. A paragraph
				// after a list belongs to the enclosing section, not its last item.
				if blocks[len(blocks)-1].owner != current() {
					add(lineStart(node), current(), current())
				}
			}
		}
	}
	visit(document, -1)
	return blocks, structured
}

// writeWebSourceEvidence replaces only the model's view of fetched Markdown.
// It preserves a contiguous verbatim prefix, accounts for ALL structural markup
// in the existing evidence budget, and returns consumed source characters (not
// rendered characters) for web_fetch continuation. Canonical tool data is intact.
func writeWebSourceEvidence(builder *strings.Builder, tag, source string, offset, maxRunes int, remaining *int) int {
	limit := max(0, maxRunes)
	if remaining != nil {
		limit = min(limit, max(0, *remaining))
	}
	prefix, sourceTruncated := truncateModelEvidence(source, limit)
	blocks, structured := webMarkdownBlocks(prefix)
	// A resumed excerpt can begin inside a code fence, table or list. Without
	// preceding syntax, parsing it as a new document would invent boundaries.
	if !structured || offset != 0 || limit < 256 {
		writeLimitedWebEvidence(builder, tag, source, limit, remaining)
		return utf8.RuneCountInString(prefix)
	}
	// The legacy ceiling was rune based; non-ASCII/escaped source could already
	// occupy more bytes. Never exceed either its rune ceiling or its byte ceiling.
	byteBudget := max(limit, len(escapeText(prefix)))
	open := fmt.Sprintf("    <%s><source_blocks offset_unit=\"unicode_char\">\n", tag)
	close := fmt.Sprintf("    </source_blocks></%s>\n", tag)
	// Reserve the omission marker even for the final block, so truncation cannot
	// itself overrun the bound. Its continuation offset is reported by the caller.
	omitted := "      <omitted reason=\"evidence_budget\" />\n"
	usedRunes := utf8.RuneCountInString(open + close + omitted)
	usedBytes := len(open + close + omitted)
	var body strings.Builder
	shownBytes := 0
	for i, block := range blocks {
		end := len(prefix)
		if i+1 < len(blocks) {
			end = blocks[i+1].start
		}
		startChar := utf8.RuneCountInString(prefix[:block.start])
		blockOpen := fmt.Sprintf("      <block offset=\"%d\"", offset+startChar)
		if block.parent >= 0 {
			parentChar := utf8.RuneCountInString(prefix[:block.parent])
			blockOpen += fmt.Sprintf(" parent_offset=\"%d\"", offset+parentChar)
		}
		blockOpen += ">"
		blockClose := "</block>\n"
		cost := blockOpen + blockClose
		availableRunes := limit - usedRunes - utf8.RuneCountInString(cost)
		availableBytes := byteBudget - usedBytes - len(cost)
		if availableRunes <= 0 || availableBytes <= 0 {
			break
		}
		consumed, rendered := webEscapedPrefix(prefix[block.start:end], availableRunes, availableBytes)
		if consumed == 0 {
			break
		}
		body.WriteString(blockOpen)
		body.WriteString(rendered)
		body.WriteString(blockClose)
		usedRunes += utf8.RuneCountInString(cost + rendered)
		usedBytes += len(cost) + len(rendered)
		shownBytes = block.start + consumed
		if shownBytes < end {
			break
		}
	}
	if shownBytes == 0 {
		// Very small budgets still make forward progress using the legacy view.
		writeLimitedWebEvidence(builder, tag, source, limit, remaining)
		return utf8.RuneCountInString(prefix)
	}
	builder.WriteString(open)
	builder.WriteString(body.String())
	if sourceTruncated || shownBytes < len(prefix) {
		builder.WriteString(omitted)
	} else {
		usedRunes -= utf8.RuneCountInString(omitted)
	}
	builder.WriteString(close)
	if remaining != nil {
		*remaining = max(0, *remaining-usedRunes)
	}
	return utf8.RuneCountInString(prefix[:shownBytes])
}

func webEscapedPrefix(source string, maxRunes, maxBytes int) (int, string) {
	var b strings.Builder
	consumed, runes, bytes := 0, 0, 0
	for index, r := range source {
		escaped := string(r)
		costRunes := 1
		switch r {
		case '&':
			escaped, costRunes = "&amp;", 5
		case '<':
			escaped, costRunes = "&lt;", 4
		case '>':
			escaped, costRunes = "&gt;", 4
		}
		if runes+costRunes > maxRunes || bytes+len(escaped) > maxBytes {
			break
		}
		b.WriteString(escaped)
		runes += costRunes
		bytes += len(escaped)
		_, width := utf8.DecodeRuneInString(source[index:])
		consumed = index + width
	}
	return consumed, b.String()
}
