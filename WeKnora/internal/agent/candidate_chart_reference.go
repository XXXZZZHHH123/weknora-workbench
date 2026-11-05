package agent

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Only an actual Markdown image destination can deliver a chart. Reuse the
// resource destination grammar used by numeric grounding, but exclude links,
// escaped examples and code. This is intentionally not a Markdown renderer:
// unsupported complex markup conservatively grants no delivery qualification.
func visibleChartImageHandles(candidate string) map[string]bool {
	hidden := chartImageExampleSpans(candidate)
	images := map[string]bool{}
	for _, match := range artifactMarkdownDestination.FindAllStringSubmatchIndex(candidate, -1) {
		if candidate[match[0]] != '!' || chartMarkdownEscaped(candidate, match[0]) {
			continue
		}
		// The shared destination grammar deliberately has no nesting rules.
		// Do not treat an image-looking substring inside another label or a
		// reference definition as a rendered image. Complex labels are outside
		// this conservative delivery check's supported inline syntax.
		labelEnd := match[0] + 2 + strings.Index(candidate[match[0]+2:match[1]], "](")
		if chartMarkdownEscaped(candidate, labelEnd) || strings.ContainsRune(candidate[match[0]+2:labelEnd], '[') {
			continue
		}
		if chartImageInsideOuterMarkup(candidate, match[0], hidden) || chartImageInsideURI(candidate[:match[0]]) {
			continue
		}
		visible := true
		for _, ignored := range hidden[match[0]:match[1]] {
			if ignored {
				visible = false
				break
			}
		}
		if !visible {
			continue
		}
		start, end := match[2], match[3]
		if start < 0 {
			start, end = match[4], match[5]
		}
		if handle, valid := types.ParseResourcePath(candidate[start:end]); valid {
			images[handle] = true
		}
	}
	return images
}

// A simple image-looking substring inside an outer label, link destination or
// title is not independently visible. Track only enough paragraph context to
// reject those cases; unsupported nested markup never grants qualification.
func chartImageInsideOuterMarkup(text string, end int, hidden []bool) bool {
	start, offset := 0, 0
	for _, line := range strings.SplitAfter(text[:end], "\n") {
		if line == "" {
			continue // The trailing split fragment is not an actual blank line.
		}
		offset += len(line)
		if strings.TrimSpace(line) == "" {
			start = offset
		}
	}
	prefix := strings.TrimLeft(text[start:end], " \t>")
	if strings.HasPrefix(prefix, "[") && strings.Index(prefix, "]:") > 0 {
		return true
	}
	brackets, parentheses := 0, 0
	var quote byte
	for i := start; i < end; i++ {
		if hidden[i] || chartMarkdownEscaped(text, i) {
			continue
		}
		if parentheses > 0 {
			if quote != 0 {
				if text[i] == quote {
					quote = 0
				}
			} else if text[i] == '\'' || text[i] == '"' {
				quote = text[i]
			} else if text[i] == '(' {
				parentheses++
			} else if text[i] == ')' {
				parentheses--
			}
			continue
		}
		if text[i] == '[' {
			brackets++
		} else if text[i] == ']' {
			if brackets > 0 {
				brackets--
			}
			if i+1 < end && text[i+1] == '(' {
				parentheses = 1
				i++
			}
		}
	}
	return brackets != 0 || parentheses != 0
}

// GFM may autolink a URI containing image-looking text. If an uninterrupted
// URI token has already begun, its body cannot independently deliver a chart.
func chartImageInsideURI(prefix string) bool {
	token := prefix[strings.LastIndexAny(prefix, " \t\r\n")+1:]
	if strings.Contains(strings.ToLower(token), "www.") {
		return true
	}
	schemeChar := func(char byte) bool {
		return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("+.-", rune(char))
	}
	for i := 0; i < len(token); i++ {
		if token[i] != ':' {
			continue
		}
		start := i
		for start > 0 && schemeChar(token[start-1]) {
			start--
		}
		if start == i || !((token[start] >= 'a' && token[start] <= 'z') || (token[start] >= 'A' && token[start] <= 'Z')) {
			continue
		}
		if strings.HasPrefix(token[i+1:], "//") || (start > 0 && token[start-1] == '<') {
			return true
		}
	}
	return false
}

func chartMarkdownEscaped(text string, position int) bool {
	backslashes := 0
	for position > 0 && text[position-1] == '\\' {
		backslashes++
		position--
	}
	return backslashes%2 != 0
}

func chartLeadingColumns(line string) int {
	columns := 0
	for _, char := range line {
		switch char {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return columns
		}
	}
	return columns
}

// Keep offsets in the original candidate. No masking/replacement can turn a
// malformed destination into a valid resource. Raw HTML and code examples
// cannot stand in for a visible image even if they contain the correct handle.
func chartImageExampleSpans(text string) []bool {
	hidden := make([]bool, len(text))
	ignore := func(start, end int) {
		for i := start; i < end; i++ {
			hidden[i] = true
		}
	}
	var fence byte
	fenceLength := 0
	htmlBlock := false
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		quoted := strings.TrimLeft(line, " \t")
		quotedCode := false
		for strings.HasPrefix(quoted, ">") {
			quoted = strings.TrimPrefix(quoted, ">")
			if strings.HasPrefix(quoted, " ") || strings.HasPrefix(quoted, "\t") {
				quoted = quoted[1:]
			}
			quotedCode = quotedCode || chartLeadingColumns(quoted) >= 4
			quoted = strings.TrimLeft(quoted, " \t")
		}
		marker := line
		for {
			marker = strings.TrimLeft(marker, " \t>")
			prefix := 0
			if len(marker) > 1 && strings.ContainsRune("-+*", rune(marker[0])) {
				prefix = 1
			} else {
				for prefix < len(marker) && marker[prefix] >= '0' && marker[prefix] <= '9' {
					prefix++
				}
				if prefix == 0 || prefix >= len(marker) || (marker[prefix] != '.' && marker[prefix] != ')') {
					break
				}
				prefix++
			}
			if prefix >= len(marker) || (marker[prefix] != ' ' && marker[prefix] != '\t') {
				break
			}
			marker = marker[prefix+1:]
		}
		run := 0
		for run < len(marker) && (marker[run] == '`' || marker[run] == '~') && marker[run] == marker[0] {
			run++
		}
		if fence != 0 {
			ignore(offset, offset+len(line))
			if run >= fenceLength && marker[0] == fence && strings.TrimSpace(marker[run:]) == "" {
				fence, fenceLength = 0, 0
			}
		} else if run >= 3 {
			fence, fenceLength = marker[0], run
			ignore(offset, offset+len(line))
		} else if quotedCode || chartLeadingColumns(line) >= 4 {
			ignore(offset, offset+len(line))
		} else {
			if trimmed == "" {
				htmlBlock = false
			} else if strings.HasPrefix(trimmed, "<") {
				htmlBlock = true
			}
			if htmlBlock {
				ignore(offset, offset+len(line))
			}
		}
		offset += len(line)
	}
	// HTML comments and raw code elements may include blank lines. Preserve
	// their whole bodies rather than treating embedded Markdown as a delivery.
	lowerBytes := []byte(text)
	for i, char := range lowerBytes {
		if char >= 'A' && char <= 'Z' {
			lowerBytes[i] = char + ('a' - 'A')
		}
	}
	lower := string(lowerBytes)
	for _, pair := range [][2]string{{"<!--", "-->"}, {"<pre", "</pre>"}, {"<code", "</code>"}, {"<script", "</script>"}, {"<style", "</style>"}, {"<textarea", "</textarea>"}} {
		for offset := 0; offset < len(text); {
			start := strings.Index(lower[offset:], pair[0])
			if start < 0 {
				break
			}
			start += offset
			end := strings.Index(lower[start+len(pair[0]):], pair[1])
			if end < 0 {
				ignore(start, len(text))
				break
			}
			end += start + len(pair[0]) + len(pair[1])
			ignore(start, end)
			offset = end
		}
	}
	// An apparent image in an HTML attribute is not rendered Markdown. Respect
	// quoted '>' characters; resource:// angle destinations are not HTML tags.
	for i := 0; i+1 < len(text); i++ {
		if text[i] != '<' {
			continue
		}
		name := i + 1
		if text[name] == '/' {
			name++
		}
		end := name
		for end < len(text) && ((lower[end] >= 'a' && lower[end] <= 'z') || (lower[end] >= '0' && lower[end] <= '9') || lower[end] == '-') {
			end++
		}
		if end == name || end >= len(text) || !strings.ContainsRune(" \t\r\n/>", rune(text[end])) {
			continue
		}
		var quote byte
		for ; end < len(text); end++ {
			if quote != 0 {
				if text[end] == quote {
					quote = 0
				}
			} else if text[end] == '\'' || text[end] == '"' {
				quote = text[end]
			} else if text[end] == '>' {
				end++
				break
			}
		}
		ignore(i, end)
		i = end - 1
	}
	for i := 0; i < len(text); {
		if text[i] != '`' || hidden[i] || chartMarkdownEscaped(text, i) {
			i++
			continue
		}
		end := i + 1
		for end < len(text) && text[end] == '`' {
			end++
		}
		length := end - i
		for next := end; next < len(text); {
			if text[next] != '`' || hidden[next] {
				next++
				continue
			}
			closeEnd := next + 1
			for closeEnd < len(text) && text[closeEnd] == '`' {
				closeEnd++
			}
			if closeEnd-next == length {
				ignore(i, closeEnd)
				end = closeEnd
				break
			}
			next = closeEnd
		}
		i = end
	}
	return hidden
}
