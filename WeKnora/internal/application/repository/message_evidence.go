package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// writeMessageEvidence participates in the same transaction as final Content.
// A nil list is an unloaded projection and must not erase durable evidence.
// Evidence IDs are immutable within a message: stale completion writers may
// not replace an observation with another payload under the same identity.
func writeMessageEvidence(tx *gorm.DB, message *types.Message) error {
	if message == nil || message.EvidenceReferences == nil {
		return nil
	}
	if len(message.EvidenceReferences) > types.MaxMessageEvidenceReferences {
		return fmt.Errorf("too many message evidence references")
	}
	if len(message.EvidenceReferences) > 0 && (message.ID == "" || message.SessionID == "" || message.Role != "assistant" || !message.IsCompleted) {
		return fmt.Errorf("evidence requires a completed assistant message")
	}
	// Compare against the row actually written by this transaction. GORM's
	// struct Updates skips zero values, including an empty incoming Content;
	// validating only the input would bind evidence to text that was not saved.
	finalContent := message.Content
	if len(message.EvidenceReferences) > 0 {
		var persisted types.Message
		if err := tx.Select("id", "content", "role", "is_completed").Where("id = ? AND session_id = ?", message.ID, message.SessionID).First(&persisted).Error; err != nil {
			return err
		}
		if persisted.Role != "assistant" || !persisted.IsCompleted || persisted.Content != message.Content {
			return fmt.Errorf("evidence does not bind the persisted assistant message")
		}
		finalContent = persisted.Content
	}
	seen := map[string]bool{}
	var stored []types.MessageEvidenceRecord
	if err := tx.Where("message_id = ? AND session_id = ?", message.ID, message.SessionID).Find(&stored).Error; err != nil {
		return err
	}
	prior := make(map[string]types.MessageEvidenceRecord, len(stored))
	for _, row := range stored {
		prior[row.EvidenceID] = row
	}
	total := 0
	for index, ref := range message.EvidenceReferences {
		if seen[ref.ID] {
			return fmt.Errorf("duplicate message evidence identity")
		}
		seen[ref.ID] = true
		if err := ref.ValidateStored(); err != nil {
			return err
		}
		if ref.CandidateHash != types.EvidenceCandidateHash(finalContent) {
			return fmt.Errorf("evidence does not bind the final message content")
		}
		payload, err := json.Marshal(ref.StorageEnvelope())
		if err != nil {
			return err
		}
		total += len(payload)
		if total > types.MaxMessageEvidenceBytes {
			return fmt.Errorf("message evidence exceeds storage budget")
		}
		digest := sha256.Sum256(payload)
		hash := hex.EncodeToString(digest[:])
		if existing, ok := prior[ref.ID]; ok {
			if existing.PayloadSHA256 != hash || existing.Payload != string(payload) || existing.Position != index {
				return fmt.Errorf("immutable message evidence changed")
			}
			continue
		}
		row := types.MessageEvidenceRecord{MessageID: message.ID, SessionID: message.SessionID, EvidenceID: ref.ID, Position: index, Payload: string(payload), PayloadSHA256: hash, CreatedAt: time.Now().UTC()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	// Explicit removal is permitted, but never recreates deleted message rows.
	query := tx.Where("message_id = ? AND session_id = ?", message.ID, message.SessionID)
	if len(seen) > 0 {
		ids := make([]string, 0, len(seen))
		for id := range seen {
			ids = append(ids, id)
		}
		query = query.Where("evidence_id NOT IN ?", ids)
	}
	return query.Delete(&types.MessageEvidenceRecord{}).Error
}

func insertMessageEvidence(tx *gorm.DB, messages []*types.Message) error {
	for _, message := range messages {
		if err := writeMessageEvidence(tx, message); err != nil {
			return err
		}
	}
	return nil
}

// attachEvidenceReferences is shared by all message loaders, including history,
// forks and search projections. Full fields have json:"-" on the public type.
func attachEvidenceReferences(ctx context.Context, db *gorm.DB, messages ...*types.Message) error {
	ids := make([]string, 0, len(messages))
	byID := make(map[string]*types.Message, len(messages))
	for _, message := range messages {
		if message == nil || message.ID == "" {
			continue
		}
		ids = append(ids, message.ID)
		byID[message.ID] = message
		message.EvidenceReferences = types.EvidenceReferences{}
	}
	for start := 0; start < len(ids); start += artifactLoadBatch {
		var rows []types.MessageEvidenceRecord
		if err := db.WithContext(ctx).Where("message_id IN ?", ids[start:min(start+artifactLoadBatch, len(ids))]).Order("message_id ASC, position ASC").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			message := byID[row.MessageID]
			if message == nil || row.SessionID != message.SessionID {
				return fmt.Errorf("evidence message scope mismatch")
			}
			digest := sha256.Sum256([]byte(row.Payload))
			if hex.EncodeToString(digest[:]) != row.PayloadSHA256 {
				return fmt.Errorf("stored evidence digest mismatch")
			}
			var stored types.StoredEvidenceEnvelope
			if err := json.Unmarshal([]byte(row.Payload), &stored); err != nil {
				return fmt.Errorf("invalid stored evidence")
			}
			ref := stored.FullReference()
			if ref.ID != row.EvidenceID {
				return fmt.Errorf("stored evidence identity mismatch")
			}
			if err := ref.ValidateStored(); err != nil {
				return err
			}
			message.EvidenceReferences = append(message.EvidenceReferences, ref)
		}
	}
	return nil
}

func deleteMessageEvidence(tx *gorm.DB, sessionID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	return tx.Where("session_id = ? AND message_id IN ?", sessionID, messageIDs).Delete(&types.MessageEvidenceRecord{}).Error
}
