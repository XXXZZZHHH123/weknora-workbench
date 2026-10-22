package types

import "time"

// ImageGenerationRequest is an authenticated tool execution, not a provider
// request supplied by the browser. Execution identity never comes from the LLM.
type ImageGenerationRequest struct {
	ModelID, SessionID, MessageID, ToolCallID string
	Prompt, Size, Quality                     string
}

type GeneratedImageArtifact struct {
	Index    int    `json:"index"`
	Handle   string `json:"handle"`
	FileName string `json:"file_name"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
}

type ImageGenerationDelivery struct {
	SessionID string                   `json:"session_id"`
	MessageID string                   `json:"message_id"`
	ModelName string                   `json:"model_name"`
	Width     int                      `json:"width"`
	Height    int                      `json:"height"`
	Artifacts []GeneratedImageArtifact `json:"artifacts"`
}

// ImageGenerationExecution is a durable claim on a paid operation. A started
// claim is never automatically submitted again: the provider may have accepted
// the request even when our connection was lost. Prompt and image bytes are
// deliberately excluded from this table.
type ImageGenerationExecution struct {
	ID          string `gorm:"type:varchar(64);primaryKey"`
	TenantID    uint64 `gorm:"uniqueIndex:ux_image_generation_message"`
	SessionID   string `gorm:"uniqueIndex:ux_image_generation_message"`
	MessageID   string `gorm:"uniqueIndex:ux_image_generation_message"`
	ToolCallID  string
	ModelID     string
	RequestHash string
	Status      string
	ModelName   string
	ResourceURL string
	FileName    string
	FileType    string
	FileSize    int64
	ContentHash string
	Width       int
	Height      int
	Position    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (ImageGenerationExecution) TableName() string { return "image_generation_executions" }
