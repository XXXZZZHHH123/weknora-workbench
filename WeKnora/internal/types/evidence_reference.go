package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	EvidenceKindRawCell          = "raw_cell"
	EvidenceKindStructuredResult = "structured_result"
	EvidenceKindDocument         = "document"
	MaxMessageEvidenceReferences = 64
	MaxEvidenceManifestBytes     = 2 << 20
	MaxMessageEvidenceBytes      = 8 << 20
)

// EvidenceSourceAuthorization is a server-observed retrieval restriction, not
// a reusable grant. Every detail request resolves current source permissions.
type EvidenceSourceAuthorization struct {
	KnowledgeIDs []string `json:"knowledge_ids,omitempty"`
	TagIDs       []string `json:"tag_ids,omitempty"`
}

// EvidenceSourceSnapshot binds an immutable observation to its original source.
// SourceSHA256 is the original bytes' SHA-256; KnowledgeFileHash, when present,
// is the knowledge record's separate legacy file hash (usually MD5).
type EvidenceSourceSnapshot struct {
	TenantID           uint64                      `json:"tenant_id,omitempty"`
	KnowledgeBaseID    string                      `json:"knowledge_base_id"`
	KnowledgeID        string                      `json:"knowledge_id"`
	SourceSHA256       string                      `json:"source_sha256,omitempty"`
	KnowledgeFileHash  string                      `json:"knowledge_file_hash,omitempty"`
	KnowledgeUpdatedAt string                      `json:"knowledge_updated_at,omitempty"`
	TableID            string                      `json:"table_id,omitempty"`
	TableRecordSHA256  string                      `json:"table_record_sha256,omitempty"`
	BundleSHA256       string                      `json:"bundle_sha256,omitempty"`
	PlanSHA256         string                      `json:"plan_sha256,omitempty"`
	ChunkID            string                      `json:"chunk_id,omitempty"`
	ContentRevision    *int                        `json:"content_revision,omitempty"`
	ContentSHA256      string                      `json:"content_sha256,omitempty"`
	Title              string                      `json:"title,omitempty"`
	FileName           string                      `json:"file_name,omitempty"`
	Locators           SourceLocators              `json:"locators,omitempty"`
	Authorization      EvidenceSourceAuthorization `json:"authorization,omitempty"`
}

// EvidenceReference is separate from SearchResult: a table observation or
// calculation is not a fabricated document chunk. Full evidence remains
// server-only on ordinary message/SSE serialization. Detail handlers project
// it explicitly only after message AND current source authorization.
// No field carries a reusable private reviewer receipt or execution privilege.
type EvidenceReference struct {
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	ResultID      string                   `json:"result_id"`
	ResultHash    string                   `json:"result_hash"`
	Title         string                   `json:"title,omitempty"`
	Summary       string                   `json:"summary,omitempty"`
	CandidateHash string                   `json:"candidate_hash,omitempty"`
	ArtifactIDs   []string                 `json:"artifact_ids,omitempty"`
	VersionStatus string                   `json:"version_status,omitempty"`
	Sources       []EvidenceSourceSnapshot `json:"-"`
	Locators      SourceLocators           `json:"-"`
	Manifest      json.RawMessage          `json:"-"`
	ClaimBindings json.RawMessage          `json:"-"`
}

type EvidenceReferences []EvidenceReference

// PublicSummary is deliberately insufficient to recreate an executable/citable
// observation. A client cannot send it back to obtain current-turn evidence.
func (ref EvidenceReference) PublicSummary() EvidenceReference {
	ref.Sources, ref.Locators, ref.Manifest, ref.ClaimBindings = nil, nil, nil, nil
	ref.ArtifactIDs = append([]string(nil), ref.ArtifactIDs...)
	return ref
}

func (refs EvidenceReferences) PublicSummaries() EvidenceReferences {
	if refs == nil {
		return nil
	}
	result := make(EvidenceReferences, len(refs))
	for i, ref := range refs {
		result[i] = ref.PublicSummary()
	}
	return result
}

// EvidenceReferencesFromInternal accepts only server-owned typed completion
// data. JSON maps/public summaries are intentionally not promoted to evidence.
func EvidenceReferencesFromInternal(values []interface{}) EvidenceReferences {
	if values == nil {
		return nil
	}
	refs := make(EvidenceReferences, 0, len(values))
	for _, value := range values {
		switch ref := value.(type) {
		case EvidenceReference:
			refs = append(refs, ref)
		case *EvidenceReference:
			if ref != nil {
				refs = append(refs, *ref)
			}
		}
	}
	return refs
}

// ValidateStored checks protocol shape only. Successful validation never
// substitutes for the agent's actual source/claim checks or current ACLs.
func (ref EvidenceReference) ValidateStored() error {
	if ref.ID == "" || len(ref.ID) > 128 || strings.ContainsAny(ref.ID, "/\\\r\n") || ref.ResultID == "" || len(ref.ResultID) > 256 {
		return fmt.Errorf("invalid evidence identity")
	}
	switch ref.Kind {
	case EvidenceKindRawCell, EvidenceKindStructuredResult, EvidenceKindDocument:
	default:
		return fmt.Errorf("invalid evidence kind")
	}
	if !evidenceHash(ref.ResultHash) || !evidenceHash(ref.CandidateHash) || len(ref.Title) > 2000 || len(ref.Summary) > 8000 || len(ref.Sources) == 0 || len(ref.Sources) > 64 {
		return fmt.Errorf("invalid evidence result or source snapshot")
	}
	if len(ref.Manifest) == 0 || len(ref.Manifest) > MaxEvidenceManifestBytes || !json.Valid(ref.Manifest) || len(ref.ClaimBindings) > 128<<10 || len(ref.ClaimBindings) > 0 && !json.Valid(ref.ClaimBindings) {
		return fmt.Errorf("invalid evidence manifest or bindings")
	}
	for _, source := range ref.Sources {
		if source.KnowledgeBaseID == "" || source.KnowledgeID == "" || len(source.KnowledgeBaseID) > 128 || len(source.KnowledgeID) > 128 {
			return fmt.Errorf("invalid evidence source identity")
		}
		if source.SourceSHA256 != "" && !evidenceHash(source.SourceSHA256) || source.ContentSHA256 != "" && !evidenceHash(source.ContentSHA256) {
			return fmt.Errorf("invalid evidence source hash")
		}
		if ref.Kind == EvidenceKindRawCell || ref.Kind == EvidenceKindStructuredResult {
			if !evidenceHash(source.SourceSHA256) {
				return fmt.Errorf("structured evidence requires an original source version")
			}
		} else if !evidenceHash(source.SourceSHA256) && (!evidenceHash(source.ContentSHA256) || source.ContentRevision == nil || *source.ContentRevision < 0) {
			return fmt.Errorf("document evidence requires an original or exact content revision")
		}
		for _, digest := range []string{source.TableRecordSHA256, source.BundleSHA256, source.PlanSHA256} {
			if digest != "" && !evidenceHash(digest) {
				return fmt.Errorf("invalid evidence table version")
			}
		}
		if len(source.Authorization.KnowledgeIDs) > 1000 || len(source.Authorization.TagIDs) > 1000 {
			return fmt.Errorf("evidence authorization scope exceeds budget")
		}
	}
	return nil
}

func evidenceHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

// EvidenceCandidateHash names the exact canonical candidate, never an answer
// approximation or a fact extracted by number matching.
func EvidenceCandidateHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// MessageEvidenceRecord stores the full snapshot separately from message JSON.
// The composite key permits a copied/forked message to retain the same durable
// evidence identity without granting access to its source message.
type MessageEvidenceRecord struct {
	MessageID     string    `gorm:"primaryKey;column:message_id"`
	EvidenceID    string    `gorm:"primaryKey;column:evidence_id"`
	SessionID     string    `gorm:"column:session_id"`
	Position      int       `gorm:"column:position"`
	Payload       string    `gorm:"column:payload"`
	PayloadSHA256 string    `gorm:"column:payload_sha256"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

func (MessageEvidenceRecord) TableName() string { return "message_evidence_records" }

// StoredEvidenceEnvelope exposes server-only fields ONLY to the persistence
// codec and the explicitly authorized detail response, not Message.MarshalJSON.
type StoredEvidenceEnvelope struct {
	Reference     EvidenceReference        `json:"reference"`
	Sources       []EvidenceSourceSnapshot `json:"sources"`
	Locators      SourceLocators           `json:"locators,omitempty"`
	Manifest      json.RawMessage          `json:"manifest"`
	ClaimBindings json.RawMessage          `json:"claim_bindings,omitempty"`
}

func (ref EvidenceReference) StorageEnvelope() StoredEvidenceEnvelope {
	return StoredEvidenceEnvelope{Reference: ref.PublicSummary(), Sources: ref.Sources, Locators: ref.Locators, Manifest: ref.Manifest, ClaimBindings: ref.ClaimBindings}
}

func (stored StoredEvidenceEnvelope) FullReference() EvidenceReference {
	ref := stored.Reference
	ref.Sources, ref.Locators, ref.Manifest, ref.ClaimBindings = stored.Sources, stored.Locators, stored.Manifest, stored.ClaimBindings
	return ref
}
