package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestEvidenceCitationsRequireExplicitCurrentRegistration(t *testing.T) {
	r := NewRegistry(true)
	history := []chat.Message{{Role: "assistant", Content: `A saved result.<evidence evidence_id="ev_saved" />`}}
	encoded := r.EncodeMessages(history)
	require.Contains(t, encoded[0].Content, `evidence://1`)
	require.Empty(t, r.CitableEvidenceHandle("ev_saved"))
	require.Empty(t, r.DecodeOutputText(`<ref id="evidence://1"/>`), "history is navigation, never a new execution witness")

	handle := r.RegisterEvidenceCitation(EvidenceCitation{ID: "ev_current", Kind: "raw_cell", Title: "Actual source"})
	require.Equal(t, "evidence://2", handle)
	require.Equal(t, handle, r.CitableEvidenceHandle("ev_current"))
	require.Equal(t, `<evidence evidence_id="ev_current" />`, r.DecodeOutputText(`<ref id="evidence://2"/>`))
	require.Empty(t, r.DecodeOutputText(`<evidence evidence_id="ev_current" />`), "raw public tags cannot authorize themselves")
	require.Empty(t, r.DecodeOutputText(`<ref id="evidence://999"/>`))
	require.Empty(t, r.RegisterEvidenceCitation(EvidenceCitation{ID: "evidence://999"}))

	r.RegisterEvidenceCitation(EvidenceCitation{ID: "ev_saved", Kind: "structured_result"})
	require.Equal(t, `<evidence evidence_id="ev_saved" />`, r.DecodeOutputText(`<ref id="evidence://1"/>`), "only an explicit current registration can promote the old navigation identity")
}

func TestEvidenceCitationsRespectDisabledAndJSONBoundaries(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		r := NewRegistry(enabled)
		r.RegisterEvidenceCitation(EvidenceCitation{ID: "ev_current", Kind: "structured_result"})
		output := r.DecodeOutputText(`{"citation":"<ref id=\"evidence://1\"/>","text":"keep \\ and \"quotes\""}`)
		var value map[string]string
		require.NoError(t, json.Unmarshal([]byte(output), &value))
		if enabled {
			require.Equal(t, `<evidence evidence_id="ev_current" />`, value["citation"])
		} else {
			require.Empty(t, value["citation"])
		}
		require.Equal(t, `keep \ and "quotes"`, value["text"])
	}
}

func TestEvidenceCitationStreamingDoesNotLeakPartialOrForgedTags(t *testing.T) {
	r := NewRegistry(true)
	r.RegisterEvidenceCitation(EvidenceCitation{ID: "ev_current", Kind: "raw_cell"})
	d := newCitationStreamExpander(r.sources)
	require.Equal(t, "Value ", d.Feed(`Value <ref id="evid`))
	require.Equal(t, `<evidence evidence_id="ev_current" /> done`, d.Feed(`ence://1"/> done`))
	require.Empty(t, d.Flush())
	require.Empty(t, d.Feed(`<evid`))
	require.Empty(t, d.Feed(`ence evidence_id="ev_current" />`))
	require.Empty(t, d.Flush())

	other := NewRegistry(true)
	require.Empty(t, other.DecodeOutputText(`<ref id="evidence://1"/>`), "handles are request-local")
}
