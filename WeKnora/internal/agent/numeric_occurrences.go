package agent

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Start and End are a half-open UTF-8 byte range in the exact checked answer.
// They are runtime observations, never locations supplied by the model.
type numericOccurrence struct {
	Token      string
	Start, End int
}

const unverifiedNumberMarker = "〔数字待核对〕"

func numericMask(value string) string {
	return strings.Repeat(" ", len(value))
}

// annotateUnverifiedNumbers inserts only at the matcher's exact byte ends.
// Goldmark's source segments restrict edits to visible prose and table cells;
// links (including labels), images, citation/HTML tags, and code stay intact.
// An unsupported token in any protected or unrecognized structure fails closed
// rather than being silently omitted or located with string replacement.
func annotateUnverifiedNumbers(answer string, occurrences []numericOccurrence) (string, int, bool) {
	if len(occurrences) == 0 || !utf8.ValidString(answer) {
		return "", 0, false
	}
	spans := append([]numericOccurrence(nil), occurrences...)
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End > spans[j].End
	})
	merged := make([]numericOccurrence, 0, len(spans))
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(answer) ||
			answer[span.Start:span.End] != span.Token ||
			!utf8.ValidString(answer[:span.Start]) || !utf8.ValidString(answer[:span.End]) {
			return "", 0, false
		}
		if len(merged) > 0 && span.Start < merged[len(merged)-1].End {
			// Unknown opaque identities can also contain unmatched decimal
			// substrings. Mark the enclosing token once, without double edits.
			if span.End <= merged[len(merged)-1].End {
				continue
			}
			return "", 0, false
		}
		merged = append(merged, span)
	}

	source := []byte(answer)
	document := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Linkify)).Parser().Parse(text.NewReader(source))
	editable := make([]bool, len(source))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.(type) {
		case *ast.Link, *ast.Image, *ast.AutoLink, *ast.CodeSpan, *ast.CodeBlock, *ast.FencedCodeBlock, *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		if content, ok := node.(*ast.Text); ok {
			segment := content.Segment
			if segment.Start >= 0 && segment.Stop <= len(source) {
				for i := segment.Start; i < segment.Stop; i++ {
					editable[i] = true
				}
			}
		}
		return ast.WalkContinue, nil
	})
	for _, span := range merged {
		for i := span.Start; i < span.End; i++ {
			if !editable[i] {
				return "", 0, false
			}
		}
	}
	var annotated strings.Builder
	annotated.Grow(len(answer) + len(merged)*len(unverifiedNumberMarker))
	previous := 0
	for _, span := range merged {
		annotated.WriteString(answer[previous:span.End])
		annotated.WriteString(unverifiedNumberMarker)
		previous = span.End
	}
	annotated.WriteString(answer[previous:])
	return annotated.String(), len(merged), true
}
