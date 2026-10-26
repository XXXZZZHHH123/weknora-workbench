// Package ingestion carries a narrowly scoped continuation of an admitted upload.
// It is independent of user credentials and the read-only structured tool protocol.
package ingestion

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const UploadPath = "/internal/ingestion/upload"
const CheckPath = "/internal/ingestion/check"
const ModelPath = "/internal/ingestion/model"

const ResultPath = "/internal/ingestion/result"

type Claim struct {
	Attempt         string `json:"attempt"`
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	UserID          string `json:"user_id"`
	FileName        string `json:"file_name"`
	FileType        string `json:"file_type"`
	FileHash        string `json:"file_hash"`
	SourceSHA256    string `json:"source_sha256"`
	BundleSHA256    string `json:"bundle_sha256"`
	FileSize        int64  `json:"file_size"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var hash32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hash64 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Claim) Validate() error {
	if !identifier.MatchString(c.Attempt) || c.TenantID == 0 || !identifier.MatchString(c.KnowledgeBaseID) || !identifier.MatchString(c.KnowledgeID) || len(c.UserID) > 128 || !hash32.MatchString(c.FileHash) || !hash64.MatchString(c.SourceSHA256) || !hash64.MatchString(c.BundleSHA256) || c.FileSize <= 0 || c.FileSize > 100<<20 || (c.FileType != "pdf" && c.FileType != "xlsx") || c.FileName == "" || len(c.FileName) > 1024 || strings.ContainsAny(c.FileName, "/\\\r\n") || !strings.HasSuffix(strings.ToLower(c.FileName), "."+c.FileType) {
		return errors.New("invalid ingestion source grant")
	}
	return nil
}
func digest(key []byte, stamp, method, path, encoded string, bodyDigest ...string) string {
	mac := hmac.New(sha256.New, key)
	parts := []string{stamp, method, path, encoded}
	if len(bodyDigest) > 0 && bodyDigest[0] != "" {
		parts = append(parts, bodyDigest[0])
	}
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}
func Sign(req *http.Request, claim Claim, key []byte, now time.Time) error {
	if len(key) < 32 {
		return errors.New("ingestion service key unavailable")
	}
	if err := claim.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(claim)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	stamp := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set("X-Ingestion-Claim", encoded)
	req.Header.Set("X-Ingestion-Timestamp", stamp)
	req.Header.Set("X-Ingestion-Signature", digest(key, stamp, req.Method, req.URL.Path, encoded, req.Header.Get("X-Ingestion-Body-SHA256")))
	return nil
}
func Verify(req *http.Request, key []byte, now time.Time) (Claim, error) {
	var claim Claim
	encoded, stamp := req.Header.Get("X-Ingestion-Claim"), req.Header.Get("X-Ingestion-Timestamp")
	ts, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || len(key) < 32 || len(encoded) > 4096 || len(stamp) > 20 || ts < now.Unix()-60 || ts > now.Unix()+60 || !hmac.Equal([]byte(req.Header.Get("X-Ingestion-Signature")), []byte(digest(key, stamp, req.Method, req.URL.Path, encoded, req.Header.Get("X-Ingestion-Body-SHA256")))) {
		return claim, errors.New("invalid ingestion signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return claim, errors.New("invalid ingestion identity")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&claim); err != nil {
		return claim, errors.New("invalid ingestion identity")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return claim, errors.New("invalid ingestion identity")
	}
	return claim, claim.Validate()
}
