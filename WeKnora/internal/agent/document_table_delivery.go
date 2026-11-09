package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	documentTableDeliveryMaxViews = 16
	documentTableDeliveryMaxBytes = 256 * 1024
)

// This pattern recognizes a technical presentation protocol, never a human
// question or a source identity. The resolver separately checks the private
// current-turn source observation and every original source/version binding.
var documentTableViewDirective = regexp.MustCompile(`^<document_table_view[ \t]+ref="([^"<>\r\n]+)"[ \t]*/>[ \t]*$`)

func (e *AgentEngine) expandDocumentTableCandidate(candidate string, state *types.AgentState) (string, int, error) {
	return expandDocumentTableDirectives(candidate, func(ref string) (string, error) {
		if state == nil {
			return "", fmt.Errorf("document table view requires current-turn execution evidence")
		}
		snapshot, err := resolveDocumentTableView(e.toolRegistry, state.RoundSteps, ref)
		if err != nil {
			return "", err
		}
		return snapshot.Markdown, nil
	})
}

type documentTableLiteralState struct {
	fence      byte
	fenceCount int
	inlineCode int
	comment    bool
	html       string
}

// Buffer only a possible protocol tag at the beginning of a Markdown line.
// A later full parse determines whether it was executable or a literal code
// example. Ordinary prose and inline/quoted examples keep their normal text.
// The stream never emits a prefix of an executable directive while waiting
// for a provider chunk to finish its ref or tag.
type documentTableStreamGuard struct {
	prefix  string
	decided bool
	held    bool
}

func (guard *documentTableStreamGuard) Feed(content string) (string, bool) {
	if guard.held {
		return "", false
	}
	var safe strings.Builder
	for i := 0; i < len(content); i++ {
		char := content[i]
		if guard.decided {
			safe.WriteByte(char)
			if char == '\n' {
				guard.decided = false
			}
			continue
		}
		guard.prefix += content[i : i+1]
		trimmed, indented := documentTableTopLevelLine(guard.prefix)
		if !indented {
			for _, marker := range []string{"<document_table_view", "</document_table_view"} {
				if trimmed == marker {
					guard.held = true
					return "", true
				}
			}
			if strings.HasPrefix("<document_table_view", trimmed) || strings.HasPrefix("</document_table_view", trimmed) {
				continue
			}
		}
		safe.WriteString(guard.prefix)
		guard.prefix = ""
		guard.decided = char != '\n'
	}
	return safe.String(), false
}

func (guard *documentTableStreamGuard) Flush() string {
	if guard.held {
		return ""
	}
	prefix := guard.prefix
	guard.prefix = ""
	return prefix
}

// Expand atomically: a mixed valid/invalid set never leaks a partly assembled
// candidate. Source cells and units are supplied only by the runtime resolver.
// Quoted, escaped, inline and code-example text has no executable authority.
func expandDocumentTableDirectives(candidate string, resolve func(string) (string, error)) (string, int, error) {
	if !strings.Contains(candidate, "<document_table_view") && !strings.Contains(candidate, "</document_table_view") {
		return candidate, 0, nil
	}
	lines := strings.SplitAfter(candidate, "\n")
	var state documentTableLiteralState
	var output strings.Builder
	count, cursor := 0, 0
	for _, line := range lines {
		cursor += len(line)
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if state.directiveLine(body, candidate[cursor:]) {
			trimmed, _ := documentTableTopLevelLine(body)
			match := documentTableViewDirective.FindStringSubmatch(trimmed)
			if len(match) != 2 || resolve == nil || count >= documentTableDeliveryMaxViews {
				return "", 0, fmt.Errorf("document table view directive is invalid or exceeds the delivery budget")
			}
			markdown, err := resolve(match[1])
			if err != nil {
				return "", 0, fmt.Errorf("document table view is not a current verified source observation: %w", err)
			}
			if strings.TrimSpace(markdown) == "" || strings.Contains(markdown, "<document_table_view") || len(markdown) > documentTableDeliveryMaxBytes {
				return "", 0, fmt.Errorf("document table view contains an invalid runtime presentation")
			}
			output.WriteString(markdown)
			if strings.HasSuffix(line, "\n") && !strings.HasSuffix(markdown, "\n") {
				output.WriteByte('\n')
			}
			count++
			if output.Len() > documentTableDeliveryMaxBytes {
				return "", 0, fmt.Errorf("expanded document table view exceeds the delivery budget")
			}
			continue
		}
		output.WriteString(line)
	}
	if count == 0 {
		return candidate, 0, nil
	}
	if output.Len() > documentTableDeliveryMaxBytes {
		return "", 0, fmt.Errorf("expanded document table view exceeds the delivery budget")
	}
	return output.String(), count, nil
}

// Share exactly the same syntax boundary between execution and private
// trace redaction; a literal example must never be transformed by either.
func (state *documentTableLiteralState) directiveLine(body, remaining string) bool {
	trimmed, indented := documentTableTopLevelLine(body)
	if state.fence != 0 {
		if !indented && documentTableFenceEnd(trimmed, state.fence, state.fenceCount) {
			state.fence, state.fenceCount = 0, 0
		}
		return false
	}
	if !indented && state.inlineCode == 0 && !state.comment && state.html == "" {
		if marker, n := documentTableFenceStart(trimmed); n >= 3 {
			state.fence, state.fenceCount = marker, n
			return false
		}
		if documentTableDirectiveLine(trimmed) {
			return true
		}
	}
	if !indented {
		state.scan(body, remaining)
	}
	return false
}

// Interrupted, tool-bearing or blocked drafts have no expanded delivery.
// Keep their ordinary text and quoted examples, but do not expose a private
// runtime directive in persisted progress/thinking cards.
func redactDocumentTableTrace(candidate string) string {
	if !strings.Contains(candidate, "document_table_view") {
		return candidate
	}
	var state documentTableLiteralState
	var output strings.Builder
	cursor := 0
	for _, line := range strings.SplitAfter(candidate, "\n") {
		cursor += len(line)
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if state.directiveLine(body, candidate[cursor:]) {
			output.WriteString("表格视图尚未展开，本轮未交付该表格。")
			if strings.HasSuffix(line, "\n") {
				output.WriteByte('\n')
			}
		} else {
			output.WriteString(line)
		}
	}
	return output.String()
}

func documentTableTopLevelLine(line string) (string, bool) {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces >= 4 || spaces < len(line) && line[spaces] == '\t' {
		return line, true
	}
	return line[spaces:], false
}

func documentTableDirectiveLine(line string) bool {
	for _, prefix := range []string{"<document_table_view", "</document_table_view"} {
		if strings.HasPrefix(line, prefix) && (len(line) == len(prefix) || strings.ContainsRune(" \t/>", rune(line[len(prefix)]))) {
			return true
		}
	}
	return false
}

func documentTableFenceStart(line string) (byte, int) {
	if len(line) == 0 || line[0] != '`' && line[0] != '~' {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	if line[0] == '`' && strings.ContainsRune(line[n:], '`') {
		return 0, 0
	}
	return line[0], n
}

func documentTableFenceEnd(line string, marker byte, minimum int) bool {
	n := 0
	for n < len(line) && line[n] == marker {
		n++
	}
	return n >= minimum && strings.TrimSpace(line[n:]) == ""
}

func (state *documentTableLiteralState) scan(line, remaining string) {
	for i := 0; i < len(line); {
		if state.comment {
			end := strings.Index(line[i:], "-->")
			if end < 0 {
				return
			}
			i += end + len("-->")
			state.comment = false
			continue
		}
		if state.html != "" {
			prefix := "</" + state.html
			lower := strings.ToLower(line[i:])
			end := strings.Index(lower, prefix)
			if end < 0 {
				return
			}
			boundary := end + len(prefix)
			if boundary == len(lower) || !strings.ContainsRune(" \t>\r", rune(lower[boundary])) {
				i += boundary
				continue
			}
			i += end
			close := strings.IndexByte(line[i:], '>')
			if close < 0 {
				return
			}
			i += close + 1
			state.html = ""
			continue
		}
		if line[i] == '\\' && state.inlineCode == 0 {
			i = min(i+2, len(line))
			continue
		}
		if line[i] == '`' {
			n := 1
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			if state.inlineCode == 0 && documentTableCodeSpanCloses(line[i+n:]+"\n"+remaining, n) {
				state.inlineCode = n
			} else if state.inlineCode == n {
				state.inlineCode = 0
			}
			i += n
			continue
		}
		if state.inlineCode == 0 && strings.HasPrefix(line[i:], "<!--") {
			state.comment = true
			i += len("<!--")
			continue
		}
		if state.inlineCode == 0 && line[i] == '<' {
			lower := strings.ToLower(line[i:])
			for _, tag := range []string{"pre", "code", "script", "style", "textarea"} {
				prefix := "<" + tag
				if strings.HasPrefix(lower, prefix) && len(lower) > len(prefix) && strings.ContainsRune(" \t>\r", rune(lower[len(prefix)])) {
					state.html = tag
					break
				}
			}
		}
		i++
	}
}

// An unmatched backtick is ordinary Markdown text. Only a complete code span
// can make a later standalone directive a literal example.
func documentTableCodeSpanCloses(text string, length int) bool {
	if boundary := strings.Index(text, "\n\n"); boundary >= 0 {
		text = text[:boundary]
	}
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		n := 1
		for i+n < len(text) && text[i+n] == '`' {
			n++
		}
		if n == length {
			return true
		}
		i += n
	}
	return false
}
