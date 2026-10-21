package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	fileService "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/models/imagegen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type imageModelFixture struct {
	interfaces.ModelService
	generator imagegen.Generator
}

func (f imageModelFixture) GetImageGenerationModel(context.Context, string) (imagegen.Generator, error) {
	return f.generator, nil
}

type imageSessionFixture struct{ interfaces.SessionRepository }

func (imageSessionFixture) Get(_ context.Context, tenant uint64, user, id string) (*types.Session, error) {
	if tenant != 7 || user != "owner" || id != "session" {
		return nil, errors.New("not owned")
	}
	return &types.Session{ID: id, TenantID: tenant, UserID: user}, nil
}

type imageGeneratorFixture struct {
	calls  atomic.Int32
	result *imagegen.Result
	err    error
}

func (g *imageGeneratorFixture) Generate(context.Context, imagegen.Request) (*imagegen.Result, error) {
	g.calls.Add(1)
	return g.result, g.err
}
func (*imageGeneratorFixture) GetModelName() string { return "configurable-image" }
func (*imageGeneratorFixture) GetModelID() string   { return "image-model" }

func newImageDeliveryFixture(t *testing.T, generator imagegen.Generator) (*ImageGenerationService, *gorm.DB, context.Context) {
	t.Helper()
	catalog, db := newResourceCatalogForTest(t)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.ImageGenerationExecution{}))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Message{ID: "message", SessionID: "session", Role: "assistant"}).Error)
	files := fileService.NewResourceCatalogFileService(fileService.NewLocalFileService(t.TempDir(), ""), catalog)
	svc := NewImageGenerationService(db, imageModelFixture{generator: generator}, imageSessionFixture{}, files, catalog)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	return svc, db, ctx
}

func imageDeliveryRequest() types.ImageGenerationRequest {
	return types.ImageGenerationRequest{SessionID: "session", MessageID: "message", ModelID: "image-model", ToolCallID: "call-1", Prompt: "An abstract blue landscape"}
}

func TestImageGenerationHTTPFixtureSavedBoundAndReplayed(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	var picture bytes.Buffer
	require.NoError(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		require.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "configured-image-name", body["model"])
		require.Equal(t, float64(1), body["n"])
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(picture.Bytes())}}})
	}))
	defer server.Close()
	g, err := imagegen.New(&imagegen.Config{ModelID: "image-model", ModelName: "configured-image-name", Provider: "generic", BaseURL: server.URL + "/v1", APIKey: "fixture-key"})
	require.NoError(t, err)
	svc, db, ctx := newImageDeliveryFixture(t, g)
	request := imageDeliveryRequest()
	delivery, err := svc.Generate(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 3, delivery.Width)
	require.Equal(t, 2, delivery.Height)
	require.Len(t, delivery.Artifacts, 1)
	artifact := delivery.Artifacts[0]
	require.Equal(t, 0, artifact.Index)
	require.Contains(t, artifact.Handle, "resource://")
	stream, err := svc.files.GetFile(ctx, artifact.Handle)
	require.NoError(t, err)
	actual, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, picture.Bytes(), actual)
	var binding types.ResourceBinding
	require.NoError(t, db.Where("owner_id = ? AND relation = ?", "message", types.ResourceRelationArtifact).First(&binding).Error)
	require.Equal(t, uint64(7), binding.TenantID)
	replayed, err := svc.Generate(ctx, request)
	require.NoError(t, err)
	require.Equal(t, delivery, replayed)
	require.Equal(t, int32(1), calls.Load(), "replay must not call the paid provider again")
	request.Prompt = "A changed request"
	_, err = svc.Generate(ctx, request)
	require.ErrorContains(t, err, "parameters changed")
	require.Equal(t, int32(1), calls.Load())
}

type imageFailingStorage struct{ interfaces.FileService }

func (imageFailingStorage) SaveBytes(context.Context, []byte, uint64, string, bool) (string, error) {
	return "", errors.New("storage unavailable")
}

func TestImageGenerationSaveFailureIsNotDeliveredOrRegenerated(t *testing.T) {
	g := &imageGeneratorFixture{result: &imagegen.Result{Data: []byte("fixture"), MIMEType: "image/png", Width: 1, Height: 1}}
	svc, db, ctx := newImageDeliveryFixture(t, g)
	svc.files = imageFailingStorage{svc.files}
	delivery, err := svc.Generate(ctx, imageDeliveryRequest())
	require.Nil(t, delivery)
	require.ErrorContains(t, err, "could not be saved")
	_, err = svc.Generate(ctx, imageDeliveryRequest())
	require.ErrorContains(t, err, "already submitted")
	require.Equal(t, int32(1), g.calls.Load())
	var count int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Count(&count).Error)
	require.Zero(t, count)
}

type imageFailingBinding struct {
	interfaces.ResourceCatalog
	fail bool
}

func (c *imageFailingBinding) Bind(ctx context.Context, ref, ownerType, ownerID, relation string) error {
	if c.fail {
		return errors.New("binding unavailable")
	}
	return c.ResourceCatalog.Bind(ctx, ref, ownerType, ownerID, relation)
}

func TestImageGenerationStoredImageCanFinishDeliveryWithoutRegeneration(t *testing.T) {
	g := &imageGeneratorFixture{result: &imagegen.Result{Data: []byte("fixture"), MIMEType: "image/png", Width: 1, Height: 1}}
	svc, _, ctx := newImageDeliveryFixture(t, g)
	bindings := &imageFailingBinding{ResourceCatalog: svc.catalog, fail: true}
	svc.catalog = bindings
	delivery, err := svc.Generate(ctx, imageDeliveryRequest())
	require.Nil(t, delivery)
	require.ErrorContains(t, err, "could not be attached")
	bindings.fail = false
	delivery, err = svc.Generate(ctx, imageDeliveryRequest())
	require.NoError(t, err)
	require.Len(t, delivery.Artifacts, 1)
	require.Equal(t, int32(1), g.calls.Load())
}

func TestImageGenerationOwnershipAndUnknownOutcome(t *testing.T) {
	g := &imageGeneratorFixture{err: errors.New("provider connection interrupted")}
	svc, _, ctx := newImageDeliveryFixture(t, g)
	request := imageDeliveryRequest()
	otherUser := context.WithValue(ctx, types.UserIDContextKey, "someone-else")
	_, err := svc.Generate(otherUser, request)
	require.ErrorContains(t, err, "session is unavailable")
	otherTenant := context.WithValue(ctx, types.TenantIDContextKey, uint64(8))
	_, err = svc.Generate(otherTenant, request)
	require.ErrorContains(t, err, "session is unavailable")
	request.MessageID = "unknown-message"
	_, err = svc.Generate(ctx, request)
	require.ErrorContains(t, err, "message is unavailable")
	require.Zero(t, g.calls.Load())
	_, err = svc.Generate(ctx, imageDeliveryRequest())
	require.ErrorContains(t, err, "will not be retried")
	_, err = svc.Generate(ctx, imageDeliveryRequest())
	require.ErrorContains(t, err, "already submitted")
	require.Equal(t, int32(1), g.calls.Load())
}

func TestImageGenerationDeletedArtifactDoesNotReappearOnReplay(t *testing.T) {
	g := &imageGeneratorFixture{result: &imagegen.Result{Data: []byte("fixture"), MIMEType: "image/png", Width: 1, Height: 1}}
	svc, db, ctx := newImageDeliveryFixture(t, g)
	_, err := svc.Generate(ctx, imageDeliveryRequest())
	require.NoError(t, err)
	repo := repository.NewMessageRepository(db)
	_, err = repo.SoftDeleteSessionArtifacts(ctx, "session", []types.ArtifactRef{{MessageID: "message", Position: 0}}, time.Now())
	require.NoError(t, err)
	_, err = svc.Generate(ctx, imageDeliveryRequest())
	require.ErrorContains(t, err, "no longer available")
	require.Equal(t, int32(1), g.calls.Load())
}

func TestImageGenerationOnePaidSubmissionPerAssistantReply(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			g := &imageGeneratorFixture{result: &imagegen.Result{Data: []byte("fixture"), MIMEType: "image/png", Width: 1, Height: 1}}
			if failed {
				g.err = errors.New("connection interrupted")
			}
			svc, db, ctx := newImageDeliveryFixture(t, g)
			request := imageDeliveryRequest()
			_, _ = svc.Generate(ctx, request)
			request.ToolCallID = "model-repair-new-call"
			_, err := svc.Generate(ctx, request)
			require.ErrorContains(t, err, "already submitted for this reply")
			require.Equal(t, int32(1), g.calls.Load())
			// A new user request produces a new assistant message and has its
			// own budget. The same prompt is not used as a cache identity.
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Message{ID: "next-message", SessionID: "session", Role: "assistant"}).Error)
			request.MessageID = "next-message"
			_, _ = svc.Generate(ctx, request)
			require.Equal(t, int32(2), g.calls.Load())
		})
	}
}
