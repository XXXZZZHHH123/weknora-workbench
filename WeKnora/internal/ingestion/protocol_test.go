package ingestion

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGrantBoundToSourceMethodPathAndTime(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	now := time.Now()
	c := Claim{Attempt: "attempt-1", TenantID: 7, KnowledgeBaseID: "kb", KnowledgeID: "doc", UserID: "user", FileName: "source.pdf", FileType: "pdf", FileHash: strings.Repeat("a", 32), SourceSHA256: strings.Repeat("b", 64), BundleSHA256: strings.Repeat("c", 64), FileSize: 32}
	req, _ := http.NewRequest("POST", "http://internal"+UploadPath, nil)
	if err := Sign(req, c, key, now); err != nil {
		t.Fatal(err)
	}
	if got, err := Verify(req, key, now); err != nil || got != c {
		t.Fatalf("%v %v", got, err)
	}
	for _, path := range []string{CheckPath, "/api/tools/read"} {
		copy := req.Clone(req.Context())
		copy.URL.Path = path
		if _, err := Verify(copy, key, now); err == nil {
			t.Fatal("grant accepted on another route")
		}
	}
	if _, err := Verify(req, key, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired grant accepted")
	}
	req.Header.Set("X-Ingestion-Claim", req.Header.Get("X-Ingestion-Claim")+"x")
	if _, err := Verify(req, key, now); err == nil {
		t.Fatal("tampered source accepted")
	}
}
