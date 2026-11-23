package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

type citationSourceView struct {
	row    map[string]any
	data   map[string]any
	result *types.ToolResult
	tool   string
}

func (view citationSourceView) currentDocumentScope(registry *agenttools.ToolRegistry) bool {
	return documentCitationRowInScope(registry, view.tool, view.result, view.row)
}

func documentCitationRowInScope(registry *agenttools.ToolRegistry, tool string, result *types.ToolResult, row map[string]any) bool {
	// The turn binding check supplements the existing evidence checks only
	// when the native document boundary is installed.
	if !agenttools.HasDocumentScope(registry) {
		return true
	}
	if tool == agenttools.ToolSearchKnowledge {
		return agenttools.IsCurrentDocumentSearchChunk(registry, result, citationString(row["knowledge_id"]))
	}
	return agenttools.IsCurrentDocumentRead(registry, result)
}

// A partial presentation is compatible only with a full body actually read
// in this turn. Claimed origin hashes cannot merge two unknown partial views.
// Selection is mechanical source identity validation, never claim matching.
func canonicalCitationFullView(views []citationSourceView) (int, bool) {
	hasProjection := false
	for _, view := range views {
		hasProjection = hasProjection || view.row["content_source_view"] != nil
	}
	if !hasProjection {
		return -1, false
	}
	for index, full := range views {
		if !citationViewIsFull(full) {
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

func citationViewsRequireProofValidation(views []citationSourceView) bool {
	projection, full := false, false
	for _, view := range views {
		projection = projection || view.row["content_source_view"] != nil
		full = full || citationViewIsFull(view)
	}
	return projection && full
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
	identity, ok := citationViewIdentity(full)
	otherIdentity, otherOK := citationViewIdentity(observed)
	body, bodyOK := full.row["content"].(string)
	content, contentOK := observed.row["content"].(string)
	if !ok || !otherOK || identity != otherIdentity || !bodyOK || !contentOK || body == "" {
		return false
	}
	rawView, projected := observed.row["content_source_view"]
	if !projected {
		return citationViewIsFull(observed) && content == body
	}
	raw, err := json.Marshal(rawView)
	var projection types.ChunkContentView
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || len(raw) > 4096 || decoder.Decode(&projection) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(projection.Ranges) == 0 || len(projection.Ranges) > types.MaxChunkContentViewRanges {
		return false
	}
	revision, _ := citationViewRevision(full.row["content_revision"])
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
