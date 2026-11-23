package agent

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// annotateUnverifiedCitations changes only exact independent inline citation
// nodes. Identity/scope qualification belongs to the caller. A citation-looking
// string in protected syntax must fail closed instead of changing code, image,
// link, attribute or table structure. Byte ranges come from the Markdown AST,
// never from a model or an unscoped string replacement.
func annotateUnverifiedCitations(answer string, invalidChunkIDs map[string]bool) (string, int, bool) {
	if !utf8.ValidString(answer) {
		return "", 0, false
	}
	type span struct{ start, end int }
	type editableCitation struct {
		span
		block ast.Node
	}
	source := []byte(answer)
	document := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Linkify)).Parser().Parse(text.NewReader(source))
	observed := []editableCitation{}
	unsafeBlocks := map[ast.Node]bool{}
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.(type) {
		case *ast.Link, *ast.Image, *ast.AutoLink, *ast.CodeSpan, *ast.CodeBlock, *ast.FencedCodeBlock, *ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		}
		raw, ok := node.(*ast.RawHTML)
		if !ok || raw.Segments.Len() == 0 {
			return ast.WalkContinue, nil
		}
		block := node.Parent()
		for block != nil && block.Type() != ast.TypeBlock {
			block = block.Parent()
		}
		first, last := raw.Segments.At(0), raw.Segments.At(raw.Segments.Len()-1)
		if first.Start < 0 || last.Stop > len(source) || first.Start >= last.Stop {
			unsafeBlocks[block] = true
			return ast.WalkContinue, nil
		}
		value := raw.Segments.Value(source)
		// A non-citation inline HTML tag can open a code/image/link container.
		// Conservatively leave that entire block untouched rather than guessing
		// HTML context from adjacent tags or malformed attribute strings.
		knowledge := documentAnswerCitation.FindSubmatch(value)
		evidence := answerEvidenceCitation.FindSubmatch(value)
		canonical := len(knowledge) == 2 && bytes.Equal(knowledge[0], value) || len(evidence) == 2 && bytes.Equal(evidence[0], value)
		if !canonical || !bytes.Equal(source[first.Start:last.Stop], value) {
			unsafeBlocks[block] = true
			return ast.WalkContinue, nil
		}
		observed = append(observed, editableCitation{span{first.Start, last.Stop}, block})
		return ast.WalkContinue, nil
	})
	editable := map[span]bool{}
	for _, item := range observed {
		if !unsafeBlocks[item.block] {
			editable[item.span] = true
		}
	}
	// An invalid destination containing raw spaces or markup may be rendered
	// by Markdown as ordinary text. Preserve that attempted destination too;
	// AST rejection must not turn embedded source bytes into an editable tag.
	linkDestinations := citationLinkDestinationRanges(answer)
	matched := map[string]bool{}
	previous, count := 0, 0
	var marked strings.Builder
	for _, location := range fallbackCitationTag.FindAllStringIndex(answer, -1) {
		tag := answer[location[0]:location[1]]
		match := documentAnswerCitation.FindStringSubmatch(tag)
		if len(match) != 2 || match[0] != tag || !invalidChunkIDs[match[1]] {
			continue
		}
		if !editable[span{location[0], location[1]}] {
			return "", 0, false
		}
		for _, destination := range linkDestinations {
			if location[0] < destination[1] && location[1] > destination[0] {
				return "", 0, false
			}
		}
		marked.WriteString(answer[previous:location[0]])
		marked.WriteString("〔引用待核对〕")
		previous = location[1]
		matched[match[1]] = true
		count++
	}
	for id, invalid := range invalidChunkIDs {
		if invalid && !matched[id] {
			return "", 0, false
		}
	}
	marked.WriteString(answer[previous:])
	return marked.String(), count, true
}

// Keep balanced inline destinations protected even when malformed markup
// prevented creation of an ast.Link. This is syntax preservation only.
func citationLinkDestinationRanges(answer string) [][2]int {
	var ranges [][2]int
	for offset := 0; offset < len(answer); {
		index := strings.Index(answer[offset:], "](")
		if index < 0 {
			break
		}
		start := offset + index + 2
		depth, end := 1, start
		for end < len(answer) && answer[end] != '\n' && depth > 0 {
			if answer[end] == '\\' && end+1 < len(answer) {
				end += 2
				continue
			}
			switch answer[end] {
			case '(':
				depth++
			case ')':
				depth--
			}
			end++
		}
		ranges = append(ranges, [2]int{start, end})
		offset = end
	}
	return ranges
}
