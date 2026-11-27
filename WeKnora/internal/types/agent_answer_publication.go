package types

import (
	"encoding/json"
	"encoding/xml"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var agentAnswerTagNames = []string{
	"evidence", "kb", "web", "ref", "task_coverage", "source_coverage",
	"runtime_event", "runtime_context", "must_use", "structured_evidence_citations",
	"tool_execution_stopped", "knowledge_chunks", "document_table_view",
}

var agentAnswerOpenEntities = []string{"&lt;", "&#60;", "&#x3c;"}
var agentAnswerCloseEntities = []string{"&gt;", "&#62;", "&#x3e;"}
var agentAnswerOpenings = append([]string{"<"}, agentAnswerOpenEntities...)
var agentAnswerOpen = "(?:<|" + strings.Join(agentAnswerOpenEntities, "|") + ")"
var agentAnswerClose = "(?:>|" + strings.Join(agentAnswerCloseEntities, "|") + ")"

// Internal source handles and runtime envelopes are for candidate review. Keep
// their spelling out of public answer text, including malformed and escaped tags.
var agentAnswerTag = regexp.MustCompile("(?i)" + agentAnswerOpen + `[[:space:]]*/?[[:space:]]*(` + strings.Join(agentAnswerTagNames, "|") + `)(?:[[:space:]/]|` + agentAnswerClose + `|$)`)
var agentAnswerTagAtStart = regexp.MustCompile("^" + agentAnswerTag.String())
var agentAnswerEntityClose = regexp.MustCompile("(?i)" + strings.Join(agentAnswerCloseEntities, "|"))
var agentAnswerClosingTags = func() map[string]*regexp.Regexp {
	tags := make(map[string]*regexp.Regexp, len(agentAnswerTagNames))
	for _, name := range agentAnswerTagNames {
		tags[name] = regexp.MustCompile("(?i)" + agentAnswerOpen + "/[[:space:]]*" + name + "[[:space:]]*" + agentAnswerClose)
	}
	return tags
}()

func HasAgentAnswerMarkup(answer string) bool {
	return agentAnswerTag.MatchString(answer) || truncatedAgentAnswerTag(answer) >= 0
}

// AgentAnswerStream holds the suffix starting at a possible tag. Streaming can
// show preceding prose immediately. Only completion has the final candidate
// and can supply attribution from its checked references.
type AgentAnswerStream struct {
	pending string
	held    bool
}

func (s *AgentAnswerStream) Feed(delta string) string {
	if s.held {
		return ""
	}
	content := s.pending + delta
	s.pending = ""
	for offset := 0; offset < len(content); {
		next := strings.IndexAny(content[offset:], "<&")
		if next < 0 {
			break
		}
		start := offset + next
		tail := content[start:]
		if partialAgentAnswerTag(tail) {
			s.pending = tail
			return content[:start]
		}
		if agentAnswerTagAtStart.MatchString(tail) {
			s.held = true
			return content[:start]
		}
		offset = start + 1
	}
	return content
}

func (s *AgentAnswerStream) Finish() string {
	pending := s.pending
	s.pending = ""
	if s.held || HasAgentAnswerMarkup(pending) {
		return ""
	}
	return pending
}

func truncatedAgentAnswerTag(content string) int {
	start := strings.LastIndexAny(content, "<&")
	if start < 0 {
		return -1
	}
	tail := content[start:]
	body, completeOpener := agentAnswerTagBody(strings.ToLower(tail))
	if !completeOpener || body == "" || !partialAgentAnswerTag(tail) {
		return -1
	}
	return start
}

func partialAgentAnswerTag(tail string) bool {
	lower := strings.ToLower(tail)
	if slices.ContainsFunc(agentAnswerOpenEntities, func(open string) bool { return strings.HasPrefix(open, lower) }) &&
		!slices.Contains(agentAnswerOpenEntities, lower) {
		return true
	}
	body, found := agentAnswerTagBody(lower)
	if !found {
		return false
	}
	return slices.ContainsFunc(agentAnswerTagNames, func(name string) bool {
		return strings.HasPrefix(name, body) || slices.ContainsFunc(agentAnswerCloseEntities, func(close string) bool {
			return body != name+close && strings.HasPrefix(name+close, body)
		})
	})
}

func agentAnswerTagBody(tail string) (string, bool) {
	for _, open := range agentAnswerOpenings {
		if body, ok := strings.CutPrefix(tail, open); ok {
			body = strings.TrimLeft(body, " \t\r\n")
			return strings.TrimLeft(strings.TrimPrefix(body, "/"), " \t\r\n"), true
		}
	}
	return "", false
}

// PublicAgentAnswer projects the canonical answer after source validation. Only
// receipts already bound to these exact bytes can be rebound to the published
// answer. Their evidence identities and private source manifests remain intact.
func PublicAgentAnswer(answer string, knowledge References, evidence EvidenceReferences) (string, EvidenceReferences) {
	return ProjectAgentAnswer(answer, knowledge, evidence, nil)
}

// StripAgentAnswerMarkup removes internal source markup from text that has no
// trusted receipts, such as an interrupted draft or a runtime notice.
func StripAgentAnswerMarkup(answer string) string {
	public, _ := ProjectAgentAnswer(answer, nil, nil, func(string) (string, bool) { return "", false })
	return public
}

// ProjectAgentAnswer uses the live web receipt resolver when supplied. The nil
// resolver is for persisted answers and server-owned completion envelopes,
// where the historical canonical web tag is the only saved URL attribution.
func ProjectAgentAnswer(answer string, knowledge References, evidence EvidenceReferences,
	webTitle func(string) (string, bool),
) (string, EvidenceReferences) {
	if !HasAgentAnswerMarkup(answer) {
		return answer, evidence
	}

	knownChunks := make(map[string]*SearchResult, len(knowledge))
	for _, ref := range knowledge {
		if ref != nil {
			knownChunks[ref.ID] = ref
		}
	}
	originalHash := EvidenceCandidateHash(answer)
	knownEvidence := make(map[string]string, len(evidence))
	for _, ref := range evidence {
		if ref.CandidateHash == originalHash {
			knownEvidence[ref.ID] = ref.Title
		}
	}

	var public strings.Builder
	positions := make(map[string][][2]int, len(knownEvidence))
	for remaining := answer; remaining != ""; {
		match := agentAnswerTag.FindStringSubmatchIndex(remaining)
		if match == nil {
			if start := truncatedAgentAnswerTag(remaining); start >= 0 {
				remaining = remaining[:start]
			}
			public.WriteString(remaining)
			break
		}
		public.WriteString(remaining[:match[0]])
		tag := remaining[match[0]:]
		name := strings.ToLower(remaining[match[2]:match[3]])
		encoded := strings.HasPrefix(tag, "&")
		end, closed := agentAnswerTagEnd(tag, encoded, match[3]-match[0])
		if !closed {
			remaining = tag[end:]
			continue
		}
		markup := tag[:end]
		remaining = tag[end:]
		switch name {
		case "evidence":
			attrs := agentAnswerTagAttributes(markup, encoded)
			id := attrs["evidence_id"]
			if title := publicSourceName(knownEvidence[id]); len(attrs) == 1 && title != "" {
				start := public.Len()
				public.WriteString("〔" + title + "〕")
				positions[id] = append(positions[id], [2]int{start, public.Len()})
			}
		case "kb":
			attrs := agentAnswerTagAttributes(markup, encoded)
			ref := knownChunks[attrs["chunk_id"]]
			if ref == nil || attrs["kb_id"] != "" && attrs["kb_id"] != ref.KnowledgeBaseID {
				continue
			}
			if title := publicSourceName(firstReferenceTitle(ref)); title != "" {
				public.WriteString("〔" + title + "〕")
			}
		case "web":
			attrs := agentAnswerTagAttributes(markup, encoded)
			if len(attrs) != 2 {
				continue
			}
			rawURL, title := attrs["url"], attrs["title"]
			if webTitle != nil {
				var verified bool
				title, verified = webTitle(rawURL)
				if !verified {
					continue
				}
			}
			if link := publicWebCitation(rawURL, title); link != "" {
				public.WriteString(link)
			}
		case "ref":
			// Unexpanded model handles have no corresponding public receipt.
		default:
			// Runtime envelopes can include structured state between the tags.
			closing := strings.Contains(markup[:match[2]-match[0]], "/")
			if !closing && !strings.HasSuffix(html.UnescapeString(markup), "/>") {
				if closeAt := agentAnswerClosingTags[name].FindStringIndex(remaining); closeAt != nil {
					remaining = remaining[closeAt[1]:]
				}
			}
		}
	}

	published := public.String()
	if HasAgentAnswerMarkup(published) {
		// Removing malformed markers can join two fragments into another tag.
		// That newly formed marker cannot carry an approved citation position.
		published = StripAgentAnswerMarkup(published)
		positions = nil
	}
	rebased := evidence
	if evidence != nil {
		rebased = make(EvidenceReferences, 0, len(evidence))
		hash := EvidenceCandidateHash(published)
		for _, ref := range evidence {
			if ref.CandidateHash != originalHash {
				continue
			}
			ref.CandidateHash = hash
			spans := positions[ref.ID]
			ref.ClaimBindings = nil
			if len(spans) > 0 || ref.ReviewStatus == "unverified" {
				ref.ClaimBindings = InlineEvidenceBindings(published, hash, spans)
			}
			if ref.ReviewStatus == "unverified" {
				ref.ClaimBindings = UnverifiedEvidenceInlineBindings(hash, ref.ClaimBindings)
			}
			rebased = append(rebased, ref)
		}
	}
	return published, rebased
}

func agentAnswerTagEnd(tag string, encoded bool, start int) (int, bool) {
	if encoded {
		if indexes := agentAnswerEntityClose.FindStringIndex(tag[start:]); indexes != nil {
			return start + indexes[1], true
		}
	} else if index := strings.IndexAny(tag[start:], "<>"); index >= 0 && tag[start+index] == '>' {
		return start + index + 1, true
	}
	if index := strings.IndexAny(tag[start:], "<\r\n"); index >= 0 {
		return start + index, false
	}
	return len(tag), false
}

func agentAnswerTagAttributes(tag string, encoded bool) map[string]string {
	if encoded {
		tag = html.UnescapeString(tag)
	}
	node, err := xml.NewDecoder(strings.NewReader(tag)).RawToken()
	start, ok := node.(xml.StartElement)
	if err != nil || !ok || start.Name.Space != "" {
		return nil
	}
	attrs := make(map[string]string, len(start.Attr))
	for _, attr := range start.Attr {
		name := strings.ToLower(attr.Name.Local)
		if _, duplicate := attrs[name]; duplicate || attr.Name.Space != "" {
			return nil
		}
		switch strings.ToLower(start.Name.Local) {
		case "evidence":
			if name != "evidence_id" {
				return nil
			}
		case "kb":
			if name != "chunk_id" && name != "kb_id" && name != "doc" {
				return nil
			}
		case "web":
			if name != "url" && name != "title" {
				return nil
			}
		}
		attrs[name] = attr.Value
	}
	return attrs
}

func firstReferenceTitle(ref *SearchResult) string {
	if strings.TrimSpace(ref.KnowledgeTitle) != "" {
		return ref.KnowledgeTitle
	}
	return ref.KnowledgeFilename
}

func publicSourceName(name string) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	return publicSourceEscapes.Replace(name)
}

var publicSourceEscapes = strings.NewReplacer(
	"&", "&amp;", "<", "＜", ">", "＞", "\\", "\\\\", "`", "\\`", "*", "\\*",
	"_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~", "|", "\\|",
)

func publicWebCitation(rawURL, title string) string {
	address, err := url.Parse(rawURL)
	if err != nil || address.Hostname() == "" || address.User != nil || (address.Scheme != "https" && address.Scheme != "http") {
		return ""
	}
	label := publicSourceName(title)
	if label == "" {
		label = publicSourceName(address.Hostname())
	}
	destination := strings.NewReplacer("<", "%3C", ">", "%3E", "(", "%28", ")", "%29", " ", "%20", "\\", "%5C").Replace(address.String())
	return "[" + label + "](" + destination + ")"
}

// InlineEvidenceBindings records byte ranges as rune positions for display.
// Both the canonical candidate and the public answer bind to their own bytes.
func InlineEvidenceBindings(answer, hash string, spans [][2]int) json.RawMessage {
	runes := []rune(answer)
	occurrences := make([]map[string]any, 0, len(spans))
	for _, span := range spans {
		start, end := utf8.RuneCountInString(answer[:span[0]]), utf8.RuneCountInString(answer[:span[1]])
		from := max(0, start-240)
		occurrences = append(occurrences, map[string]any{
			"start_rune": start, "end_rune": end, "context_start_rune": from, "adjacent_context": string(runes[from:start]),
		})
	}
	encoded, _ := json.Marshal(map[string]any{
		"protocol": "inline_evidence_v1", "candidate_sha256": hash, "occurrences": occurrences,
		"notice": "The model chose the inline citation. These positions bind the exact final text; adjacent context is navigation, not automatic semantic proof.",
	})
	return encoded
}

func UnverifiedEvidenceInlineBindings(hash string, inline json.RawMessage) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{
		"protocol": "unverified_navigation_v1", "candidate_sha256": hash,
		"navigation_only": true, "review_status": "unverified", "inline": inline,
		"notice": "Current execution observations for user checking. The answer has not completed semantic review; an inline position is not proof of its adjacent claim.",
	})
	return encoded
}

// PublicAgentMessage is a read-only view for older assistant rows. Authorization
// and evidence detail continue to read the original message and its stored hash.
func PublicAgentMessage(message *Message) *Message {
	if message == nil || message.Role != "assistant" {
		return message
	}
	projectAnswer := VerifiedPublicationEnabled() && (message.AgentID != "" || len(message.AgentSteps) != 0 ||
		len(message.EvidenceReferences) != 0) && HasAgentAnswerMarkup(message.Content)
	projectReason := message.RunState == MessageRunStateEvidencePartial && message.RunReason != message.ExecutionContext.EvidencePartialReason
	if !projectAnswer && !projectReason {
		return message
	}
	copy := *message
	if projectReason {
		copy.RunReason = message.ExecutionContext.EvidencePartialReason
	}
	if projectAnswer {
		copy.Content, copy.EvidenceReferences = PublicAgentAnswer(message.Content, message.KnowledgeReferences, message.EvidenceReferences)
	}
	return &copy
}

func PublicAgentMessages(messages []*Message) []*Message {
	var public []*Message
	for i, message := range messages {
		if view := PublicAgentMessage(message); message != view {
			if public == nil {
				public = slices.Clone(messages)
			}
			public[i] = view
		}
	}
	if public == nil {
		return messages
	}
	return public
}
