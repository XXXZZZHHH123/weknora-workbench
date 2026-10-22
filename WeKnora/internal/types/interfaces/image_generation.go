package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type ImageGenerationService interface {
	Generate(ctx context.Context, request types.ImageGenerationRequest) (*types.ImageGenerationDelivery, error)
}
