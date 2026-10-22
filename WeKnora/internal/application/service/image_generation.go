package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/models/imagegen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type imageGenerationModels interface {
	GetImageGenerationModel(context.Context, string) (imagegen.Generator, error)
}

// ImageGenerationService persists an execution claim before contacting the
// provider, and persists/binds the resulting file before reporting success.
type ImageGenerationService struct {
	db       *gorm.DB
	models   imageGenerationModels
	sessions interfaces.SessionRepository
	files    interfaces.FileService
	catalog  interfaces.ResourceCatalog
}

func NewImageGenerationService(db *gorm.DB, models interfaces.ModelService, sessions interfaces.SessionRepository,
	files interfaces.FileService, catalog interfaces.ResourceCatalog,
) *ImageGenerationService {
	return &ImageGenerationService{db: db, models: models, sessions: sessions, files: files, catalog: catalog}
}

func imageGenerationHash(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *ImageGenerationService) Generate(ctx context.Context, request types.ImageGenerationRequest) (*types.ImageGenerationDelivery, error) {
	if s == nil || s.db == nil || s.models == nil || s.sessions == nil || s.files == nil || s.catalog == nil {
		return nil, errors.New("image generation is unavailable")
	}
	_, ok := types.TenantIDFromContext(ctx)
	tenantID := types.CallerFromContext(ctx).TenantID
	if !ok || tenantID == 0 || request.ModelID == "" || request.SessionID == "" || request.MessageID == "" || request.ToolCallID == "" {
		return nil, errors.New("image generation requires an authenticated message and tool execution")
	}
	if len(request.Prompt) > 32000 || strings.TrimSpace(request.Prompt) == "" || len(request.Size) > 32 || len(request.Quality) > 32 {
		return nil, errors.New("invalid image generation parameters")
	}
	// Shared agents may resolve models in their source workspace, but generated
	// files and execution claims belong to the authenticated caller's session.
	sessionCtx := context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ownerID := types.SessionOwnerIDFromContext(sessionCtx)
	session, err := s.sessions.Get(sessionCtx, tenantID, ownerID, request.SessionID)
	if err != nil || session == nil || session.TenantID != tenantID || session.UserID != ownerID {
		return nil, errors.New("image generation session is unavailable")
	}
	var message types.Message
	if err := s.db.WithContext(ctx).Where("id = ? AND session_id = ? AND role = ?", request.MessageID, request.SessionID, "assistant").First(&message).Error; err != nil {
		return nil, errors.New("image generation assistant message is unavailable")
	}
	// Resolve the authorized model before consuming a claim; invalid credentials
	// and configuration can be corrected without creating a paid execution.
	model, err := s.models.GetImageGenerationModel(ctx, request.ModelID)
	if err != nil {
		return nil, errors.New("configured image generation model is unavailable")
	}
	ctx = sessionCtx
	record := types.ImageGenerationExecution{
		ID:       imageGenerationHash([]any{tenantID, request.SessionID, request.MessageID, request.ToolCallID}),
		TenantID: tenantID, SessionID: request.SessionID, MessageID: request.MessageID,
		ToolCallID: request.ToolCallID, ModelID: request.ModelID,
		RequestHash: imageGenerationHash([]string{request.ModelID, request.Prompt, request.Size, request.Quality}),
		Status:      "started", ModelName: model.GetModelName(), Position: -1,
	}
	claim := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if claim.Error != nil {
		return nil, errors.New("image generation execution could not be recorded")
	}
	if claim.RowsAffected == 0 {
		var existing types.ImageGenerationExecution
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND session_id = ? AND message_id = ?", tenantID, request.SessionID, request.MessageID).First(&existing).Error; err != nil {
			return nil, errors.New("image generation execution could not be loaded")
		}
		// This first phase delivers one image per assistant reply. A model or
		// reviewer may otherwise repeat a failed paid action with a new call ID.
		// The database constraint applies this budget across workers/restarts.
		if existing.ToolCallID != request.ToolCallID {
			return nil, errors.New("one image generation was already submitted for this reply; start a new user request to generate another image")
		}
		if existing.RequestHash != record.RequestHash {
			return nil, errors.New("image generation execution parameters changed; use a new tool execution")
		}
		switch existing.Status {
		case "completed":
			return s.delivery(ctx, existing)
		case "stored":
			return s.attach(ctx, existing)
		default:
			return nil, errors.New("image generation was already submitted; its outcome may be unknown, so it will not be submitted again automatically")
		}
	}
	generated, err := model.Generate(ctx, imagegen.Request{Prompt: request.Prompt, Size: request.Size, Quality: request.Quality})
	if err != nil {
		// Do not persist a provider error which might include a URL or secret.
		s.markFailed(ctx, record.ID)
		var providerError *imagegen.ProviderError
		if errors.As(err, &providerError) && providerError != nil {
			// This type exposes only adapter-validated status/code/parameter
			// metadata. Discard the original wrapper: it may contain arbitrary
			// transport messages, signed URLs or credentials.
			return nil, fmt.Errorf("%w; the request will not be retried automatically", providerError)
		}
		return nil, errors.New("image generation failed or was interrupted; the request will not be retried automatically")
	}
	if generated == nil || len(generated.Data) == 0 || generated.Width <= 0 || generated.Height <= 0 {
		s.markFailed(ctx, record.ID)
		return nil, errors.New("image generation returned no valid image")
	}
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[generated.MIMEType]
	if ext == "" {
		s.markFailed(ctx, record.ID)
		return nil, errors.New("image generation returned an unsupported image format")
	}
	// Finish delivery even if the user cancels immediately after the provider
	// returns. This bounded context cannot submit a second paid request.
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	record.FileName, record.FileType = "generated-"+record.ID[:16]+ext, ext
	record.FileSize = int64(len(generated.Data))
	record.Width, record.Height = generated.Width, generated.Height
	sum := sha256.Sum256(generated.Data)
	record.ContentHash = hex.EncodeToString(sum[:])
	record.ResourceURL, err = s.files.SaveBytes(deliveryCtx, generated.Data, tenantID, record.FileName, false)
	if err != nil {
		s.markFailed(deliveryCtx, record.ID)
		return nil, errors.New("image was generated but could not be saved; no downloadable image was delivered")
	}
	if _, ok := types.ParseResourcePath(record.ResourceURL); !ok {
		_ = s.files.DeleteFile(deliveryCtx, record.ResourceURL)
		s.markFailed(deliveryCtx, record.ID)
		return nil, errors.New("image storage did not provide a persistent resource reference")
	}
	record.Status = "stored"
	if err := s.db.WithContext(deliveryCtx).Model(&types.ImageGenerationExecution{}).Where("id = ?", record.ID).Updates(map[string]any{
		"status": record.Status, "resource_url": record.ResourceURL, "file_name": record.FileName,
		"file_type": record.FileType, "file_size": record.FileSize, "content_hash": record.ContentHash,
		"width": record.Width, "height": record.Height,
	}).Error; err != nil {
		_ = s.files.DeleteFile(deliveryCtx, record.ResourceURL)
		return nil, errors.New("image delivery could not be recorded; generation will not be repeated automatically")
	}
	return s.attach(deliveryCtx, record)
}

func (s *ImageGenerationService) markFailed(ctx context.Context, id string) {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.db.WithContext(updateCtx).Model(&types.ImageGenerationExecution{}).Where("id = ? AND status = ?", id, "started").Update("status", "failed").Error
}

func (s *ImageGenerationService) attach(ctx context.Context, record types.ImageGenerationExecution) (*types.ImageGenerationDelivery, error) {
	if err := s.catalog.Bind(ctx, record.ResourceURL, types.ResourceOwnerMessage, record.MessageID, types.ResourceRelationArtifact); err != nil {
		return nil, errors.New("image was saved but could not be attached; retry this execution to finish delivery")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message types.Message
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND session_id = ? AND role = ?", record.MessageID, record.SessionID, "assistant").First(&message).Error; err != nil {
			return err
		}
		var existing types.MessageArtifactRecord
		if err := tx.Where("message_id = ? AND url = ?", record.MessageID, record.ResourceURL).First(&existing).Error; err == nil {
			record.Position = existing.Position
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else {
			var highest struct{ Max int }
			if err := tx.Model(&types.MessageArtifactRecord{}).Select("COALESCE(MAX(position), -1) AS max").Where("message_id = ?", record.MessageID).Scan(&highest).Error; err != nil {
				return err
			}
			record.Position = highest.Max + 1
			art := types.MessageArtifact{URL: record.ResourceURL, FileName: record.FileName, FileType: record.FileType, SourcePath: "image-generation://" + record.ID,
				FileSize: record.FileSize, ContentHash: record.ContentHash, CreatedAt: record.CreatedAt}
			row := types.NewMessageArtifactRecords(record.SessionID, record.MessageID, types.MessageArtifacts{art})[0]
			row.Position = record.Position
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Model(&types.ImageGenerationExecution{}).Where("id = ?", record.ID).Updates(map[string]any{"status": "completed", "position": record.Position}).Error
	})
	if err != nil {
		return nil, errors.New("image was saved but delivery could not be committed; retry this execution to finish delivery")
	}
	return s.delivery(ctx, record)
}

func (s *ImageGenerationService) delivery(ctx context.Context, record types.ImageGenerationExecution) (*types.ImageGenerationDelivery, error) {
	var artifact types.MessageArtifactRecord
	if err := s.db.WithContext(ctx).Where("message_id = ? AND position = ? AND url = ? AND deleted_at IS NULL", record.MessageID, record.Position, record.ResourceURL).First(&artifact).Error; err != nil {
		return nil, errors.New("generated image is no longer available")
	}
	return &types.ImageGenerationDelivery{SessionID: record.SessionID, MessageID: record.MessageID,
		ModelName: record.ModelName, Width: record.Width, Height: record.Height,
		Artifacts: []types.GeneratedImageArtifact{{Index: artifact.Position, Handle: artifact.URL,
			FileName: artifact.FileName, FileType: artifact.FileType, FileSize: artifact.FileSize}}}, nil
}
