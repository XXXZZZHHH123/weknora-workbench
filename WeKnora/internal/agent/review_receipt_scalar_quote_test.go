package agent

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewWitnessQuotePreservesJSONScalarSpelling(t *testing.T) {
	for _, spelling := range []string{"12.50", "1e+09", "900719925474099312345678901", "true", "false"} {
		record, reader, raw := receiptFixture(t, "scalar-wire", `{"success":true,"data":{"value":`+spelling+`}}`)
		var witness reviewSourceWitness
		require.NoError(t, json.Unmarshal([]byte(`{"evidence_id":"`+record.ID+`","json_pointer":"/data/value","quote":`+spelling+`,"kind":"source"}`), &witness))
		require.Equal(t, spelling, witness.Quote)
		require.Equal(t, record.ID, witness.EvidenceID)
		require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
		require.Error(t, reviewWitnessVisible(map[string]any{}, reader, witness), "wire normalization cannot expose hidden values")
	}
}
func TestReviewWitnessQuoteStillRejectsCompositeOrNullAndWrongSourceValues(t *testing.T) {
	for _, spelling := range []string{`null`, `[]`, `{}`, `{"value":12}`} {
		var witness reviewSourceWitness
		require.Error(t, json.Unmarshal([]byte(`{"quote":`+spelling+`}`), &witness))
	}
	record, reader, raw := receiptFixture(t, "wrong-wire", `{"success":true,"data":{"value":12.50}}`)
	var witness reviewSourceWitness
	require.NoError(t, json.Unmarshal([]byte(`{"evidence_id":"`+record.ID+`","json_pointer":"/data/value","quote":12.51,"kind":"source"}`), &witness))
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
}
