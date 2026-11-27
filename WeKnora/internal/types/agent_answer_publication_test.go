package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentAnswerStreamKeepsOrdinaryMarkupAndWaitsAtCitation(t *testing.T) {
	for _, test := range []struct {
		name   string
		deltas []string
		want   []string
	}{
		{"ordinary", []string{"Uses A &", " B, where x <", " 3. <em>", "Result 42</em>."}, []string{"Uses A ", "& B, where x ", "< 3. <em>", "Result 42</em>."}},
		{"split raw", []string{"Observed 42<", "evi", "dence evidence_id=\"ev\"/>", " end."}, []string{"Observed 42", "", "", ""}},
		{"split escaped", []string{"Observed 42&l", "t;k", "b chunk_id=\"chunk\" /&gt;"}, []string{"Observed 42", "", ""}},
		{"split numeric", []string{"Observed 42&#", "x3c;k", "b chunk_id=\"chunk\" /&#x3e;"}, []string{"Observed 42", "", ""}},
		{"reserved prefix disambiguated", []string{"View <r", "uby>help</ruby>."}, []string{"View ", "<ruby>help</ruby>."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stream AgentAnswerStream
			for i, delta := range test.deltas {
				require.Equal(t, test.want[i], stream.Feed(delta))
			}
		})
	}
}

func TestPublicAgentAnswerBindsExactUTF8PresentationAndRejectsUnqualifiedMarkers(t *testing.T) {
	const candidate = "数量🙂 42<evidence evidence_id=\"ev\"/> <kb doc=\"spoof\" chunk_id=\"read\" kb_id=\"current\"/>；" +
		"日期后补<evidence evidence_id=\"ev\" />。<kb chunk_id=\"read\" kb_id=\"old\"/>" +
		"<evidence evidence_id=\"stale\"/><ref id=\"chunk://99\"/>"
	refs := EvidenceReferences{
		{ID: "ev", CandidateHash: EvidenceCandidateHash(candidate), Title: "台账_1.xlsx", Manifest: json.RawMessage(`{"known":true}`)},
		{ID: "stale", CandidateHash: EvidenceCandidateHash("earlier answer"), Title: "Never publish"},
	}
	knowledge := References{{ID: "read", KnowledgeBaseID: "current", KnowledgeTitle: "本期周报.pdf"}}
	answer, projected := PublicAgentAnswer(candidate, knowledge, refs)
	require.Equal(t, "数量🙂 42〔台账\\_1.xlsx〕 〔本期周报.pdf〕；日期后补〔台账\\_1.xlsx〕。", answer)
	require.False(t, HasAgentAnswerMarkup(answer))
	require.Len(t, projected, 1)
	require.Equal(t, refs[0].ID, projected[0].ID)
	require.Equal(t, refs[0].Manifest, projected[0].Manifest)
	require.Equal(t, EvidenceCandidateHash(candidate), refs[0].CandidateHash)
	require.Equal(t, EvidenceCandidateHash(answer), projected[0].CandidateHash)

	var claim struct {
		Protocol    string `json:"protocol"`
		Candidate   string `json:"candidate_sha256"`
		Occurrences []struct {
			Start int    `json:"start_rune"`
			End   int    `json:"end_rune"`
			From  int    `json:"context_start_rune"`
			Text  string `json:"adjacent_context"`
		} `json:"occurrences"`
	}
	require.NoError(t, json.Unmarshal(projected[0].ClaimBindings, &claim))
	require.Equal(t, "inline_evidence_v1", claim.Protocol)
	require.Equal(t, projected[0].CandidateHash, claim.Candidate)
	require.Len(t, claim.Occurrences, 2)
	runes := []rune(answer)
	for _, occurrence := range claim.Occurrences {
		require.Equal(t, "〔台账\\_1.xlsx〕", string(runes[occurrence.Start:occurrence.End]))
		require.Equal(t, string(runes[occurrence.From:occurrence.Start]), occurrence.Text)
	}
}

func TestPublicAgentAnswerKeepsPublicMarkdownAndDropsPrivateArtifacts(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"42", "42"},
		{"x < 3, A & B, and <em>ordinary</em>. ![chart](resource://known)", "x < 3, A & B, and <em>ordinary</em>. ![chart](resource://known)"},
		{"42 &lt;kb chunk_id=&quot;spoof&quot; /&gt; tails", "42  tails"},
		{"42 &#60;kb chunk_id=&quot;spoof&quot; /&#x3e; tails", "42  tails"},
		{"42 <EVIDENCE evidence_id=\"stale\"\nNext line", "42 \nNext line"},
		{"42<evide", "42"},
		{"42<knowledge_chunks>PRIVATE</knowledge_chunks> next", "42 next"},
		{"42<knowledge_chunks>PRIVATE&lt;/knowledge_chunks &gt; next", "42 next"},
		{"42 <<ref id=\"chunk://99\"/>kb chunk_id=\"bad\"/> remaining", "42  remaining"},
		{"42 <web url=\"javascript:alert(1)\" title=\"do not link\" />", "42 "},
		{"42 <web url=\"https://example.invalid/a(b)?c=d&amp;e=f\" title=\"Source\" />", "42 [Source](https://example.invalid/a%28b%29?c=d&e=f)"},
	} {
		answer, refs := PublicAgentAnswer(test.input, nil, nil)
		require.Equal(t, test.want, answer, test.input)
		require.Empty(t, refs)
		require.False(t, HasAgentAnswerMarkup(answer), test.input)
	}
}
