package session

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnverifiedEvidenceDetailStatusCannotWaiveCurrentACLOrSourceVersion(t *testing.T) {
	_, knowledge, message, router := evidenceHandlerFixture(t)
	message.EvidenceReferences[0].ReviewStatus = "unverified"
	path := "/sessions/session/messages/message/evidence/ev_source"
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	w := get(path)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"review_status":"unverified"`)
	require.Contains(t, w.Body.String(), `"status":"snapshot"`)
	require.NotContains(t, w.Body.String(), "PRIVATE_")
	knowledge.tags = nil
	reads := knowledge.reads
	w = get(path)
	require.Contains(t, w.Body.String(), `"status":"unavailable"`)
	require.NotContains(t, w.Body.String(), `"manifest"`)
	require.Equal(t, reads, knowledge.reads)
	w = get(path + "/sources/0/preview")
	require.NotEqual(t, http.StatusOK, w.Code)
	require.Equal(t, reads, knowledge.reads)
	knowledge.tags = []string{"doc"}
	knowledge.raw = []byte("%PDF-1.7\nchanged original")
	w = get(path)
	require.Contains(t, w.Body.String(), `"status":"stale"`)
	require.NotContains(t, w.Body.String(), `"manifest"`)
	w = get(path + "/sources/0/preview")
	require.NotEqual(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "changed original")
}
