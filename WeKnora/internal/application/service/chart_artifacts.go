package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChartArtifactService stores server-rendered data charts in the same protected
// artifact library as other generated files. It has no provider or code runner.
type ChartArtifactService struct {
	db       *gorm.DB
	sessions interfaces.SessionRepository
	files    interfaces.FileService
	catalog  interfaces.ResourceCatalog
}

func NewChartArtifactService(db *gorm.DB, sessions interfaces.SessionRepository, files interfaces.FileService, catalog interfaces.ResourceCatalog) *ChartArtifactService {
	return &ChartArtifactService{db: db, sessions: sessions, files: files, catalog: catalog}
}

var _ chart.Writer = (*ChartArtifactService)(nil)

func chartDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func chartDelivery(record types.MessageArtifactRecord, digest string) (*chart.Delivery, error) {
	if record.ContentHash != digest {
		return nil, errors.New("chart execution parameters changed; use a new tool execution")
	}
	if _, ok := types.ParseResourcePath(record.URL); !ok || record.DeletedAt != nil {
		return nil, errors.New("saved chart is no longer available")
	}
	return &chart.Delivery{Handle: record.URL, Index: record.Position, FileName: record.FileName}, nil
}

func (s *ChartArtifactService) SaveChart(ctx context.Context, request chart.SaveRequest) (*chart.Delivery, error) {
	if s == nil || s.db == nil || s.sessions == nil || s.files == nil || s.catalog == nil {
		return nil, errors.New("chart storage is unavailable")
	}
	tenantID := types.CallerFromContext(ctx).TenantID
	if tenantID == 0 || request.SessionID == "" || request.MessageID == "" || request.ToolCallID == "" {
		return nil, errors.New("chart storage requires an authenticated message and tool execution")
	}
	if len(request.HTML) == 0 || len(request.HTML) > chart.MaxHTMLBytes || request.SourceID == "" || len(request.SourceID) > 1024 {
		return nil, errors.New("invalid chart document or source identity")
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.SessionTenantIDContextKey, nil)
	ownerID := types.SessionOwnerIDFromContext(ctx)
	session, err := s.sessions.Get(ctx, tenantID, ownerID, request.SessionID)
	if err != nil || session == nil || session.TenantID != tenantID || session.UserID != ownerID {
		return nil, errors.New("chart session is unavailable")
	}
	var message types.Message
	if err := s.db.WithContext(ctx).Where("id = ? AND session_id = ? AND role = ?", request.MessageID, request.SessionID, "assistant").First(&message).Error; err != nil {
		return nil, errors.New("chart assistant message is unavailable")
	}
	identity, _ := json.Marshal([]any{tenantID, request.SessionID, request.MessageID, request.ToolCallID})
	sourcePath := "data-chart://" + chartDigest(identity)
	digest := chartDigest(request.HTML)
	var existing types.MessageArtifactRecord
	lookup := s.db.WithContext(ctx).Where("session_id = ? AND message_id = ? AND source_path = ?", request.SessionID, request.MessageID, sourcePath).First(&existing)
	if lookup.Error == nil {
		return chartDelivery(existing, digest)
	}
	if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return nil, errors.New("saved chart could not be checked")
	}
	// Completing a local delivery after cancellation cannot submit a paid action.
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	fileName := "chart-" + digest[:16] + ".html"
	reference, err := s.files.SaveBytes(deliveryCtx, request.HTML, tenantID, fileName, false)
	if err != nil {
		return nil, errors.New("chart could not be saved; no downloadable chart was delivered")
	}
	if _, ok := types.ParseResourcePath(reference); !ok {
		return nil, errors.New("chart storage did not provide a persistent resource reference")
	}
	if err := s.catalog.Bind(deliveryCtx, reference, types.ResourceOwnerMessage, request.MessageID, types.ResourceRelationArtifact); err != nil {
		s.releaseUnattachedChart(deliveryCtx, request.MessageID, reference)
		return nil, errors.New("chart could not be attached to its message")
	}
	var record types.MessageArtifactRecord
	err = s.db.WithContext(deliveryCtx).Transaction(func(tx *gorm.DB) error {
		// Serialize positions and replay checks across workers with the owning
		// message lock. Storage/catalog I/O stays outside this DB transaction.
		var current types.Message
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND session_id = ? AND role = ?", request.MessageID, request.SessionID, "assistant").First(&current).Error; err != nil {
			return err
		}
		found := tx.Where("session_id = ? AND message_id = ? AND source_path = ?", request.SessionID, request.MessageID, sourcePath).First(&record)
		if found.Error == nil {
			_, err := chartDelivery(record, digest)
			return err
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		var highest struct{ Max int }
		if err := tx.Model(&types.MessageArtifactRecord{}).Select("COALESCE(MAX(position), -1) AS max").Where("message_id = ?", request.MessageID).Scan(&highest).Error; err != nil {
			return err
		}
		record = types.NewMessageArtifactRecords(request.SessionID, request.MessageID, types.MessageArtifacts{{URL: reference, FileName: fileName, FileType: ".html", FileSize: int64(len(request.HTML)), ContentHash: digest, SourcePath: sourcePath}})[0]
		record.Position = highest.Max + 1
		return tx.Create(&record).Error
	})
	if err != nil {
		s.releaseUnattachedChart(deliveryCtx, request.MessageID, reference)
		return nil, errors.New("chart delivery could not be committed")
	}
	if record.URL != reference {
		s.releaseUnattachedChart(deliveryCtx, request.MessageID, reference)
	}
	return chartDelivery(record, digest)
}

func (s *ChartArtifactService) releaseUnattachedChart(ctx context.Context, messageID, reference string) {
	// SaveBytes may deduplicate a resource. Never release an existing artifact's
	// message claim merely because another concurrent execution reused it.
	var count int64
	if err := s.db.WithContext(ctx).Model(&types.MessageArtifactRecord{}).Where("message_id = ? AND url = ?", messageID, reference).Count(&count).Error; err != nil || count != 0 {
		return
	}
	if remaining, err := s.catalog.Release(ctx, reference, types.ResourceOwnerMessage, messageID); err == nil && remaining == 0 {
		_ = s.files.DeleteFile(ctx, reference)
	}
}
