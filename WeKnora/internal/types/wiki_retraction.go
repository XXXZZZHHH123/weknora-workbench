package types

import (
	"context"
	"encoding/json"
	"sort"
)

const wikiRetractionMetadataKey = "_wiki_retraction"

type WikiRetractionState struct {
	PendingSourceIDs []string `json:"pending_source_ids,omitempty"`
	FailedSourceIDs  []string `json:"failed_source_ids,omitempty"`
}

type wikiRetractionMetadataMutationKey struct{}

// WithWikiRetractionMetadataMutation marks an internal write that intentionally
// changes the durable retraction marker. Ordinary page writes preserve markers
// from the current stored page, preventing stale snapshots from clearing them.
func WithWikiRetractionMetadataMutation(ctx context.Context) context.Context {
	return context.WithValue(ctx, wikiRetractionMetadataMutationKey{}, true)
}

func WikiRetractionMetadataMutationAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(wikiRetractionMetadataMutationKey{}).(bool)
	return allowed
}

func ReadWikiRetractionState(page *WikiPage) WikiRetractionState {
	if page == nil || len(page.PageMetadata) == 0 {
		return WikiRetractionState{}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(page.PageMetadata, &envelope); err != nil || envelope == nil {
		return WikiRetractionState{}
	}
	var state WikiRetractionState
	if raw := envelope[wikiRetractionMetadataKey]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &state)
	}
	state.PendingSourceIDs = uniqueSortedStrings(state.PendingSourceIDs)
	state.FailedSourceIDs = uniqueSortedStrings(state.FailedSourceIDs)
	return state
}

func SetWikiRetractionSource(page *WikiPage, sourceID string, failed bool) {
	if page == nil || sourceID == "" {
		return
	}
	state := ReadWikiRetractionState(page)
	state.PendingSourceIDs = removeString(state.PendingSourceIDs, sourceID)
	state.FailedSourceIDs = removeString(state.FailedSourceIDs, sourceID)
	if failed {
		state.FailedSourceIDs = append(state.FailedSourceIDs, sourceID)
	} else {
		state.PendingSourceIDs = append(state.PendingSourceIDs, sourceID)
	}
	writeWikiRetractionState(page, state)
}

func FailPendingWikiRetractionSource(page *WikiPage, sourceID string) bool {
	if page == nil || sourceID == "" {
		return false
	}
	state := ReadWikiRetractionState(page)
	wasPending := false
	for _, pendingID := range state.PendingSourceIDs {
		if pendingID == sourceID {
			wasPending = true
			break
		}
	}
	if !wasPending {
		return false
	}
	SetWikiRetractionSource(page, sourceID, true)
	return true
}

func ClearWikiRetractionSources(page *WikiPage, sourceIDs []string) {
	if page == nil || len(sourceIDs) == 0 {
		return
	}
	state := ReadWikiRetractionState(page)
	for _, sourceID := range sourceIDs {
		state.PendingSourceIDs = removeString(state.PendingSourceIDs, sourceID)
		state.FailedSourceIDs = removeString(state.FailedSourceIDs, sourceID)
	}
	writeWikiRetractionState(page, state)
}

// PreserveWikiRetractionState preserves the stored marker as the sole
// authority. An ordinary caller's stale or user-authored metadata cannot
// create, clear, or reactivate a system marker.
func PreserveWikiRetractionState(incoming, current JSON) JSON {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(incoming, &envelope); err != nil || envelope == nil {
		envelope = make(map[string]json.RawMessage)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(current, &stored); err != nil || stored == nil {
		stored = nil
	}
	if raw := stored[wikiRetractionMetadataKey]; len(raw) > 0 {
		envelope[wikiRetractionMetadataKey] = append(json.RawMessage(nil), raw...)
	} else {
		delete(envelope, wikiRetractionMetadataKey)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return incoming
	}
	return JSON(encoded)
}

func writeWikiRetractionState(page *WikiPage, state WikiRetractionState) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(page.PageMetadata, &envelope); err != nil || envelope == nil {
		envelope = make(map[string]json.RawMessage)
	}
	state.PendingSourceIDs = uniqueSortedStrings(state.PendingSourceIDs)
	state.FailedSourceIDs = uniqueSortedStrings(state.FailedSourceIDs)
	if len(state.PendingSourceIDs) == 0 && len(state.FailedSourceIDs) == 0 {
		delete(envelope, wikiRetractionMetadataKey)
	} else {
		encoded, _ := json.Marshal(state)
		envelope[wikiRetractionMetadataKey] = encoded
	}
	encoded, err := json.Marshal(envelope)
	if err == nil {
		page.PageMetadata = JSON(encoded)
	}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}
