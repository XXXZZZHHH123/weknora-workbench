package types

import (
	"encoding/json"
	"time"
)

const KnowledgeFileVersionsMetadataKey = "_file_versions_v1"

// KnowledgeFileVersion records the identity of a superseded upload.
type KnowledgeFileVersion struct {
	FileName   string    `json:"file_name"`
	FileHash   string    `json:"file_hash"`
	FileSize   int64     `json:"file_size"`
	UploadedAt time.Time `json:"uploaded_at"`
	ReplacedAt time.Time `json:"replaced_at"`
	ReplacedBy string    `json:"replaced_by,omitempty"`
}

// KnowledgeFileVersionHistory groups the last upload's timestamp and earlier
// versions. The timestamp is independent of later metadata or processing edits.
type KnowledgeFileVersionHistory struct {
	UpdatedAt             time.Time              `json:"updated_at"`
	Previous              []KnowledgeFileVersion `json:"previous"`
	LastPublishedFileHash string                 `json:"last_published_file_hash,omitempty"`
}

func (k *Knowledge) FileVersionHistory() (KnowledgeFileVersionHistory, error) {
	var history KnowledgeFileVersionHistory
	if len(k.Metadata) == 0 {
		return history, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(k.Metadata, &fields); err != nil {
		return history, err
	}
	// Older uploads could attach arbitrary string-valued metadata. Only
	// version objects written by the replacement service carry provenance.
	if raw := fields[KnowledgeFileVersionsMetadataKey]; len(raw) != 0 && raw[0] == '{' {
		return history, json.Unmarshal(raw, &history)
	}
	return history, nil
}

func (k *Knowledge) SetFileVersionHistory(history KnowledgeFileVersionHistory) error {
	fields, err := k.Metadata.Map()
	if err != nil {
		return err
	}
	if fields == nil {
		fields = make(map[string]interface{})
	}
	fields[KnowledgeFileVersionsMetadataKey] = history
	encoded, err := json.Marshal(fields)
	if err == nil {
		k.Metadata = JSON(encoded)
	}
	return err
}
