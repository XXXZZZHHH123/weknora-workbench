package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type citationSourceView struct {
	row  map[string]any
	data map[string]any
}

// A partial presentation is compatible only with a full body actually read
// in this turn. Claimed origin hashes cannot merge two unknown partial views.
// Selection is mechanical source identity validation, never claim matching.
func canonicalCitationFullView(views []citationSourceView) (int, bool) {
	if len(views) < 2 {
		return -1, false
	}
	for index, full := range views {
		if _, known := citationViewIdentity(full); !known || !citationViewIsFull(full) {
			continue
		}
		compatible := true
		for _, observed := range views {
			if !citationViewMatchesFull(full, observed) {
				compatible = false
				break
			}
		}
		if compatible {
			return index, true
		}
	}
	return -1, false
}

// A legacy full observation may lack a revision while an actual current page
// read has complete identity and an exact projection proof. Verify the current
// page against the full bytes, but return only that current page as canonical.
// The legacy body's extra ranges never inherit the page's identity or scope.
func canonicalCitationWitnessView(views []citationSourceView) (int, bool) {
	if index, ok := canonicalCitationFullView(views); ok {
		return index, true
	}
	for index, current := range views {
		if _, known := citationViewIdentity(current); !known || current.row["content_source_view"] == nil {
			continue
		}
		revision, _ := citationViewRevision(current.row["content_revision"])
		for _, legacy := range views {
			if _, known := citationViewIdentity(legacy); known || !citationViewIsFull(legacy) || !citationViewKnownIdentityMatches(current, legacy) {
				continue
			}
			body, ok := legacy.row["content"].(string)
			if !ok || body == "" || !citationProjectionMatchesBody(body, revision, current) {
				continue
			}
			compatible := true
			for _, observed := range views {
				if !citationViewKnownIdentityMatches(current, observed) {
					compatible = false
					break
				}
				if observed.row["content_source_view"] != nil {
					compatible = citationProjectionMatchesBody(body, revision, observed)
				} else {
					compatible = citationViewIsFull(observed) && observed.row["content"] == body
				}
				if !compatible {
					break
				}
			}
			if compatible {
				return index, true
			}
		}
	}
	return -1, false
}

func citationViewsRequireProofValidation(views []citationSourceView) bool {
	if len(views) < 2 {
		return false
	}
	projection, full := false, false
	for _, view := range views {
		projection = projection || view.row["content_source_view"] != nil
		full = full || citationViewIsFull(view)
		if _, known := citationViewIdentity(view); known && citationViewIsFull(view) {
			return true
		}
	}
	return projection && full
}

func citationIncompleteIdentityViewCount(views []citationSourceView) int {
	count := 0
	for _, view := range views {
		if _, known := citationViewIdentity(view); !known {
			count++
		}
	}
	return count
}

func citationViewIsFull(view citationSourceView) bool {
	offset, known := citationViewRevision(view.row["content_offset"])
	return known && offset == 0 && view.row["content_complete"] == true &&
		view.row["content_offset_space"] == "chunk_unicode_runes" && view.row["content_rewritten"] != true
}

func citationViewRevision(value any) (int, bool) {
	if value == nil {
		return 0, false
	}
	raw, err := json.Marshal(value)
	var revision int
	if err != nil || json.Unmarshal(raw, &revision) != nil || revision < 0 {
		return 0, false
	}
	return revision, true
}

func citationViewIdentity(view citationSourceView) (string, bool) {
	id, document := citationString(view.row["chunk_id"]), citationString(view.row["knowledge_id"])
	library := citationString(view.row["knowledge_base_id"])
	if library == "" {
		library = citationString(view.row["knowledge_base"])
	}
	if alias := citationString(view.row["knowledge_base"]); library != "" && alias != "" && alias != library {
		return "", false
	}
	if citationString(view.data["knowledge_id"]) == document {
		parentLibrary := citationString(view.data["knowledge_base_id"])
		if library != "" && parentLibrary != "" && library != parentLibrary {
			return "", false
		}
		if library == "" {
			library = parentLibrary
		}
	}
	revision, known := citationViewRevision(view.row["content_revision"])
	if id == "" || document == "" || library == "" || !known || view.row["content_rewritten"] == true {
		return "", false
	}
	return reviewDigest([]any{id, document, library, revision, view.row["source_locators"],
		view.row["start_at"], view.row["end_at"], view.row["chunk_type"], view.row["parent_chunk_id"], view.row["chunk_index"]}), true
}

func citationViewMatchesFull(full, observed citationSourceView) bool {
	_, ok := citationViewIdentity(full)
	body, bodyOK := full.row["content"].(string)
	content, contentOK := observed.row["content"].(string)
	if !ok || !citationViewKnownIdentityMatches(full, observed) || !bodyOK || !contentOK || body == "" {
		return false
	}
	_, projected := observed.row["content_source_view"]
	if !projected {
		// Old tools may omit completeness/offset metadata. Exact whole-body
		// equality with a current full witness is enough; explicit partial or
		// nonzero offset claims must still agree with that presentation.
		if offset, present := observed.row["content_offset"]; present {
			value, valid := citationViewRevision(offset)
			if !valid || value != 0 {
				return false
			}
		}
		if observed.row["content_complete"] == false {
			return false
		}
		return content == body
	}
	revision, _ := citationViewRevision(full.row["content_revision"])
	return citationProjectionMatchesBody(body, revision, observed)
}

func citationProjectionMatchesBody(body string, revision int, observed citationSourceView) bool {
	content, contentOK := observed.row["content"].(string)
	rawView, projected := observed.row["content_source_view"]
	if !contentOK || !projected {
		return false
	}
	raw, err := json.Marshal(rawView)
	var projection types.ChunkContentView
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || len(raw) > 4096 || decoder.Decode(&projection) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(projection.Ranges) == 0 || len(projection.Ranges) > types.MaxChunkContentViewRanges {
		return false
	}
	digest := sha256.Sum256([]byte(body))
	if projection.OriginRevision != revision || projection.OriginSHA256 != hex.EncodeToString(digest[:]) ||
		projection.OriginRuneCount != utf8.RuneCountInString(body) {
		return false
	}
	runes := []rune(body)
	var expected strings.Builder
	end := 0
	for _, span := range projection.Ranges {
		if span.Start < end || span.Start < 0 || span.End <= span.Start || span.End > len(runes) {
			return false
		}
		expected.WriteString(string(runes[span.Start:span.End]))
		end = span.End
	}
	return expected.String() == content
}

// Even two unprojected equal bodies can be from different revisions or
// libraries. Compare explicitly supplied fields before selecting any witness.
func citationViewsExplicitIdentityConflict(views []citationSourceView) bool {
	known := map[string]string{}
	for _, view := range views {
		for _, field := range []string{"chunk_id", "knowledge_id", "content_revision", "chunk_type", "parent_chunk_id", "chunk_index", "start_at", "end_at", "source_locators"} {
			value, present := view.row[field]
			if !present || value == nil {
				continue
			}
			if field == "content_revision" {
				revision, valid := citationViewRevision(value)
				if !valid {
					return true
				}
				value = revision
			}
			digest := reviewDigest(value)
			if prior, exists := known[field]; exists && prior != digest {
				return true
			}
			known[field] = digest
		}
		libraries := []string{citationString(view.row["knowledge_base_id"]), citationString(view.row["knowledge_base"])}
		if citationString(view.data["knowledge_id"]) == citationString(view.row["knowledge_id"]) {
			libraries = append(libraries, citationString(view.data["knowledge_base_id"]))
		}
		for _, library := range libraries {
			if library == "" {
				continue
			}
			if prior, exists := known["library"]; exists && prior != library {
				return true
			}
			known["library"] = library
		}
	}
	return false
}

// Missing identity is not conflicting identity. A legacy observation can be
// reconciled only against a current complete witness, and every identity field
// it actually supplies must agree. This does not establish new source facts.
func citationViewKnownIdentityMatches(full, observed citationSourceView) bool {
	if observed.row["content_rewritten"] == true {
		return false
	}
	for _, field := range []string{"chunk_id", "knowledge_id", "chunk_type", "parent_chunk_id", "chunk_index", "start_at", "end_at", "source_locators"} {
		if value, present := observed.row[field]; present && value != nil {
			if reviewDigest(value) != reviewDigest(full.row[field]) {
				return false
			}
		}
	}
	if value, present := observed.row["content_revision"]; present && value != nil {
		revision, valid := citationViewRevision(value)
		current, currentValid := citationViewRevision(full.row["content_revision"])
		if !valid || !currentValid || revision != current {
			return false
		}
	}
	// Validate aliases and parent metadata independently; an explicit
	// contradictory library cannot hide behind an otherwise valid row.
	fullLibrary := citationString(full.row["knowledge_base_id"])
	if fullLibrary == "" {
		fullLibrary = citationString(full.row["knowledge_base"])
	}
	if fullLibrary == "" {
		fullLibrary = citationString(full.data["knowledge_base_id"])
	}
	for _, field := range []string{"knowledge_base_id", "knowledge_base"} {
		if library := citationString(observed.row[field]); library != "" && library != fullLibrary {
			return false
		}
	}
	if citationString(observed.data["knowledge_id"]) == citationString(full.row["knowledge_id"]) {
		if library := citationString(observed.data["knowledge_base_id"]); library != "" && library != fullLibrary {
			return false
		}
	}
	return true
}
