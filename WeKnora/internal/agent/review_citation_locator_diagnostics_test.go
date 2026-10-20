package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Synthetic body text; the ellipsis quote reproduces the observed protocol defect.
func citationLocatorInput() map[string]any {
	return map[string]any{"citation_source_evidence": map[string]any{
		"items": []any{
			map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "产量 | 265 | 271 | 263 | -7"},
			map[string]any{"source_pointer_ref": 2, "content_included": false, "content": ""},
		},
		"source_pointers": []any{[]any{0, "/data/chunks/0"}, []any{0, "/data/chunks/1"}, []any{0, "/data/chunks/2"}},
		"source_results":  []any{[]any{"read_document", "call", "id"}},
	}}
}

func TestCitationLocatorDiagnosticsNameTheActualDefect(t *testing.T) {
	input := citationLocatorInput()
	ref := func(n int) *int { return &n }

	_, err := resolveCitationBodyWitness(input, nil, reviewSourceWitness{SourcePointerRef: ref(3), Quote: "263", Kind: "source"})
	require.ErrorContains(t, err, "source_pointer_ref 3")
	require.ErrorContains(t, err, "[0,2]")

	_, err = resolveCitationBodyWitness(input, nil, reviewSourceWitness{SourcePointerRef: ref(0), Quote: "产量 | 265 | ... | 263 | -7", Kind: "source"})
	require.ErrorContains(t, err, "exact contiguous substring")
	require.ErrorContains(t, err, "ellipses")

	_, err = resolveCitationBodyWitness(input, nil, reviewSourceWitness{SourcePointerRef: ref(2), Quote: "263", Kind: "source"})
	require.ErrorContains(t, err, "not displayed")
}
