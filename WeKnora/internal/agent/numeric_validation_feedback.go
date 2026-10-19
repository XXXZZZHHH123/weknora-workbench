package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	numericValidationFeedbackBudget       = 4096
	numericValidationFeedbackIssueLimit   = 8
	numericValidationFeedbackContextLimit = 120
	numericValidationFeedbackTokenLimit   = 128
)

type numericValidationFeedbackContextKey struct{}

var numericValidationFeedbackOpaqueToken = sourceOpaqueToken

type numericValidationFeedbackOccurrence struct {
	StartByte             int    `json:"start_byte"`
	EndByte               int    `json:"end_byte"`
	StartRune             int    `json:"start_rune"`
	EndRune               int    `json:"end_rune"`
	ContextStartRune      int    `json:"context_start_rune"`
	ContextEndRune        int    `json:"context_end_rune"`
	ContextTruncatedLeft  bool   `json:"context_truncated_left"`
	ContextTruncatedRight bool   `json:"context_truncated_right"`
	Context               string `json:"context"`
}

type numericValidationFeedbackIssue struct {
	Token                     string                                `json:"token"`
	TokenTruncated            bool                                  `json:"token_truncated,omitempty"`
	TokenKind                 string                                `json:"token_kind"`
	CandidateOccurrences      []numericValidationFeedbackOccurrence `json:"candidate_occurrences"`
	OmittedOccurrenceCount    int                                   `json:"omitted_occurrence_count"`
	NoLexicalCandidateLocated bool                                  `json:"no_lexical_candidate_located,omitempty"`
}

type numericValidationFeedbackView struct {
	Kind                         string                           `json:"kind"`
	CandidateSHA256              string                           `json:"candidate_sha256"`
	CheckStatus                  string                           `json:"check_status"`
	UnsupportedTokenCount        int                              `json:"unsupported_token_count"`
	UnsupportedTokens            []numericValidationFeedbackIssue `json:"unsupported_tokens,omitempty"`
	OmittedUnsupportedTokenCount int                              `json:"omitted_unsupported_token_count,omitempty"`
	OffsetUnits                  string                           `json:"offset_units"`
	ContextMaxRunes              int                              `json:"context_max_runes"`
	OmittedContextCount          int                              `json:"omitted_context_count,omitempty"`
	Notice                       string                           `json:"notice"`
}

// buildNumericValidationFeedback turns the deterministic numeric guard's
// exact unmatched tokens into bounded lexical locations in the candidate.
// Locations are diagnostic only: the guard may already have masked a source
// identity or filename, so an occurrence is not independently asserted to be
// the offending claim.
func buildNumericValidationFeedback(content string, unsupported []string) map[string]any {
	unique := make([]string, 0, len(unsupported))
	seen := make(map[string]struct{}, len(unsupported))
	for _, token := range unsupported {
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		unique = append(unique, token)
	}

	digest := sha256.Sum256([]byte(content))
	view := numericValidationFeedbackView{
		Kind:                  "deterministic_numeric_validation",
		CandidateSHA256:       hex.EncodeToString(digest[:]),
		CheckStatus:           "no_unmatched_numbers",
		UnsupportedTokenCount: len(unique),
		OffsetUnits:           "byte and Unicode rune offsets; start inclusive, end exclusive",
		ContextMaxRunes:       numericValidationFeedbackContextLimit,
		Notice:                "Program validation diagnostic only, not source evidence or a semantic verdict. Locations are lexical candidates in the original draft; the numeric guard may have masked known identities or filenames before reporting a token. A missing location is unknown, not proof of absence.",
	}
	if len(unique) > 0 {
		view.CheckStatus = "unsupported_tokens"
	}

	runes := []rune(content)
	for i, token := range unique {
		if i >= numericValidationFeedbackIssueLimit {
			view.OmittedUnsupportedTokenCount = len(unique) - i
			break
		}
		issue := numericValidationFeedbackIssue{
			Token:                token,
			TokenKind:            "number",
			CandidateOccurrences: []numericValidationFeedbackOccurrence{},
		}
		if strings.ContainsAny(token, "abcdefABCDEF") && numericValidationFeedbackOpaqueToken.MatchString(token) && numericValidationFeedbackOpaqueToken.FindString(token) == token {
			issue.TokenKind = "opaque_identifier"
		}
		issue.Token, issue.TokenTruncated = truncateNumericFeedbackToken(token, numericValidationFeedbackTokenLimit)

		matches, matchCount := numericFeedbackTokenMatches(content, token, issue.TokenKind, 2)
		if len(matches) == 0 {
			issue.NoLexicalCandidateLocated = true
		} else {
			// A small number of exact occurrences is enough to locate repeated
			// claims while keeping the critic input bounded.
			for _, match := range matches {
				startRune := utf8.RuneCountInString(content[:match[0]])
				endRune := utf8.RuneCountInString(content[:match[1]])
				occurrence := numericValidationFeedbackOccurrence{StartByte: match[0], EndByte: match[1], StartRune: startRune, EndRune: endRune}
				occurrence.ContextStartRune, occurrence.ContextEndRune, occurrence.Context = numericFeedbackContext(runes, startRune, endRune, numericValidationFeedbackContextLimit)
				occurrence.ContextTruncatedLeft = occurrence.ContextStartRune > 0
				occurrence.ContextTruncatedRight = occurrence.ContextEndRune < len(runes)
				issue.CandidateOccurrences = append(issue.CandidateOccurrences, occurrence)
			}
			issue.OmittedOccurrenceCount = matchCount - len(matches)
		}
		view.UnsupportedTokens = append(view.UnsupportedTokens, issue)
	}

	// Keep a hard serialized-size ceiling even for adversarially long candidate
	// tokens and repeated matches. Every reduction is represented by omission
	// counters or the final notice; no text is silently treated as complete.
	for {
		encoded, _ := json.Marshal(view)
		if len(encoded) <= numericValidationFeedbackBudget {
			return numericFeedbackAsMap(view)
		}
		if view.ContextMaxRunes > 0 {
			view.ContextMaxRunes -= 8
			if view.ContextMaxRunes < 0 {
				view.ContextMaxRunes = 0
			}
			view.OmittedContextCount = 0
			for i := range view.UnsupportedTokens {
				for j := range view.UnsupportedTokens[i].CandidateOccurrences {
					o := &view.UnsupportedTokens[i].CandidateOccurrences[j]
					start, end, contextText := numericFeedbackContext(runes, o.StartRune, o.EndRune, view.ContextMaxRunes)
					if start != o.ContextStartRune || end != o.ContextEndRune || contextText != o.Context {
						view.OmittedContextCount++
					}
					o.ContextStartRune, o.ContextEndRune, o.Context = start, end, contextText
					o.ContextTruncatedLeft = start > 0
					o.ContextTruncatedRight = end < len(runes)
				}
			}
			continue
		}
		if view.OmittedUnsupportedTokenCount < view.UnsupportedTokenCount {
			view.UnsupportedTokens = view.UnsupportedTokens[:len(view.UnsupportedTokens)-1]
			view.OmittedUnsupportedTokenCount++
			continue
		}
		// The fixed envelope itself is small; this final conservative fallback
		// is only reachable for exceptionally large externally supplied tokens.
		view.UnsupportedTokens = nil
		view.OmittedUnsupportedTokenCount = view.UnsupportedTokenCount
		view.Notice = "Program validation diagnostic only, not source evidence or a semantic verdict. Token locations omitted to respect the diagnostic byte budget."
	}
}

func numericFeedbackTokenMatches(content, token, kind string, limit int) ([][2]int, int) {
	var pattern *regexp.Regexp
	if kind == "opaque_identifier" {
		pattern = numericValidationFeedbackOpaqueToken
	} else {
		pattern = proseNumber
	}
	matches := make([][2]int, 0, 2)
	count := 0
	for offset := 0; offset < len(content); {
		span := pattern.FindStringIndex(content[offset:])
		if span == nil {
			break
		}
		start, end := offset+span[0], offset+span[1]
		if content[start:end] == token {
			count++
			if len(matches) < limit {
				matches = append(matches, [2]int{start, end})
			}
		}
		if end <= offset {
			break
		}
		offset = end
	}
	return matches, count
}

func numericFeedbackContext(content []rune, startRune, endRune, limit int) (int, int, string) {
	if limit < 0 {
		limit = 0
	}
	if startRune < 0 {
		startRune = 0
	}
	if endRune > len(content) {
		endRune = len(content)
	}
	if endRune < startRune {
		endRune = startRune
	}
	if endRune-startRune > limit {
		endRune = startRune + limit
	}
	remaining := limit - (endRune - startRune)
	left := remaining / 2
	contextStart := startRune - left
	contextEnd := endRune + (remaining - left)
	if contextStart < 0 {
		contextEnd = min(len(content), contextEnd-contextStart)
		contextStart = 0
	}
	if contextEnd > len(content) {
		contextStart = max(0, contextStart-(contextEnd-len(content)))
		contextEnd = len(content)
	}
	if contextStart > startRune {
		contextStart = startRune
	}
	if contextEnd < endRune {
		contextEnd = endRune
	}
	return contextStart, contextEnd, string(content[contextStart:contextEnd])
}

func truncateNumericFeedbackToken(token string, limit int) (string, bool) {
	if len(token) <= limit {
		return token, false
	}
	if limit < 1 {
		return "", true
	}
	for limit > 0 && !utf8.ValidString(token[:limit]) {
		limit--
	}
	return token[:limit], true
}

func numericFeedbackAsMap(view numericValidationFeedbackView) map[string]any {
	encoded, _ := json.Marshal(view)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

// withNumericValidationFeedback carries an immutable typed diagnostic across
// the critic call without mixing it into source results or conversation rows.
func withNumericValidationFeedback(ctx context.Context, feedback map[string]any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if feedback == nil {
		return ctx
	}
	copy := numericValidationFeedbackDeepCopy(feedback)
	if copy == nil {
		return ctx
	}
	return context.WithValue(ctx, numericValidationFeedbackContextKey{}, copy)
}

func numericValidationFeedbackFromContext(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	feedback, _ := ctx.Value(numericValidationFeedbackContextKey{}).(map[string]any)
	if feedback == nil {
		return nil
	}
	return numericValidationFeedbackDeepCopy(feedback)
}

func numericValidationFeedbackDeepCopy(feedback map[string]any) map[string]any {
	encoded, err := json.Marshal(feedback)
	if err != nil || len(encoded) > numericValidationFeedbackBudget {
		return nil
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return nil
	}
	return copy
}

func numericValidationFeedbackMatchesCandidate(feedback map[string]any, content string) bool {
	if feedback == nil {
		return false
	}
	digest := sha256.Sum256([]byte(content))
	return feedback["candidate_sha256"] == hex.EncodeToString(digest[:])
}
