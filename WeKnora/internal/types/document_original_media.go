package types

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// OriginalMediaReference is navigation to a relationship in an immutable source,
// not a saved resource or proof of authorization. Every use must revalidate it.
const OriginalMediaPrefix = "original-media://"

type OriginalMediaReference struct {
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	SourceSHA256    string `json:"source_sha256"`
	Page            int    `json:"page"`
	Part            string `json:"part"`
	MediaSHA256     string `json:"media_sha256"`
}
type OriginalDocumentImage struct {
	Reference      string `json:"reference"`
	Page           int    `json:"page"`
	Part           string `json:"part"`
	Label          string `json:"label"`
	LabelTruncated bool   `json:"label_truncated,omitempty"`
}

func (s OriginalMediaReference) Reference() string {
	b, _ := json.Marshal(s)
	return OriginalMediaPrefix + base64.RawURLEncoding.EncodeToString(b)
}
func ParseOriginalMediaReference(ref string) (OriginalMediaReference, error) {
	var s OriginalMediaReference
	if !strings.HasPrefix(ref, OriginalMediaPrefix) || len(ref) > 4096 {
		return s, errors.New("invalid original media reference")
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(ref, OriginalMediaPrefix))
	if e != nil {
		return s, e
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&s); e != nil {
		return s, e
	}
	if s.KnowledgeID == "" || s.KnowledgeBaseID == "" || s.Page < 1 || len(s.SourceSHA256) != 64 || len(s.MediaSHA256) != 64 || s.Part == "" || s.Reference() != ref {
		return s, errors.New("noncanonical original media reference")
	}
	return s, nil
}

// An explicit limit failure is not evidence that the remaining figures are absent.
type OriginalMediaCatalogLimitError struct{ Total, Limit int }

func (e *OriginalMediaCatalogLimitError) Error() string {
	return fmt.Sprintf("original page catalog has %d images, limit %d; read fewer physical pages", e.Total, e.Limit)
}
