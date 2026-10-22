package service

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func chartSaveFixture() chart.SaveRequest {
	return chart.SaveRequest{SessionID: "session", MessageID: "message", ToolCallID: "chart-call", SourceID: "actual-table", FileName: "../../ignored.html", HTML: []byte(`<!doctype html><html><body><svg xmlns="http://www.w3.org/2000/svg"><text>Source data chart</text></svg></body></html>`)}
}

func TestChartArtifactsSavedBoundAndReplayPreservesOtherArtifacts(t *testing.T) {
	imageSvc, db, ctx := newImageDeliveryFixture(t, &imageGeneratorFixture{})
	svc := NewChartArtifactService(db, imageSvc.sessions, imageSvc.files, imageSvc.catalog)
	prior := types.NewMessageArtifactRecords("session", "message", types.MessageArtifacts{{URL: "resource://abcdefghijklmnopqrstuv", FileName: "prior.png", FileType: ".png"}})[0]
	require.NoError(t, db.Create(&prior).Error)
	request := chartSaveFixture()
	delivery, err := svc.SaveChart(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 1, delivery.Index)
	require.Regexp(t, `^chart-[0-9a-f]{16}\.html$`, delivery.FileName)
	stream, err := svc.files.GetFile(ctx, delivery.Handle)
	require.NoError(t, err)
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, request.HTML, data)
	var binding types.ResourceBinding
	require.NoError(t, db.Where("owner_id = ? AND relation = ?", "message", types.ResourceRelationArtifact).First(&binding).Error)
	require.Equal(t, uint64(7), binding.TenantID)
	replayed, err := svc.SaveChart(ctx, request)
	require.NoError(t, err)
	require.Equal(t, delivery, replayed)
	var count int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	request.HTML = []byte("changed document")
	_, err = svc.SaveChart(ctx, request)
	require.ErrorContains(t, err, "parameters changed")
	var unchanged types.MessageArtifactRecord
	require.NoError(t, db.First(&unchanged, "id = ?", prior.ID).Error)
	require.Equal(t, prior.URL, unchanged.URL)
}

func TestChartArtifactsCallerOwnershipAndSharedExecution(t *testing.T) {
	imageSvc, db, ctx := newImageDeliveryFixture(t, &imageGeneratorFixture{})
	svc := NewChartArtifactService(db, imageSvc.sessions, imageSvc.files, imageSvc.catalog)
	for _, bad := range []context.Context{
		context.WithValue(ctx, types.UserIDContextKey, "another-user"),
		context.WithValue(ctx, types.TenantIDContextKey, uint64(8)),
		types.WithCaller(ctx, types.Caller{}),
	} {
		_, err := svc.SaveChart(bad, chartSaveFixture())
		require.Error(t, err)
	}
	wrongMessage := chartSaveFixture()
	wrongMessage.MessageID = "other-message"
	_, err := svc.SaveChart(ctx, wrongMessage)
	require.ErrorContains(t, err, "assistant message")
	shared := types.WithExecutionTenant(ctx, 99)
	result, err := svc.SaveChart(shared, chartSaveFixture())
	require.NoError(t, err)
	require.NotEmpty(t, result.Handle)
	var binding types.ResourceBinding
	require.NoError(t, db.Where("owner_id = ?", "message").First(&binding).Error)
	require.Equal(t, uint64(7), binding.TenantID)
}

func TestChartArtifactsStorageFailuresAndDeletedReplay(t *testing.T) {
	imageSvc, db, ctx := newImageDeliveryFixture(t, &imageGeneratorFixture{})
	svc := NewChartArtifactService(db, imageSvc.sessions, imageSvc.files, imageSvc.catalog)
	svc.files = imageFailingStorage{svc.files}
	delivery, err := svc.SaveChart(ctx, chartSaveFixture())
	require.Nil(t, delivery)
	require.ErrorContains(t, err, "could not be saved")
	svc.files = imageSvc.files
	svc.catalog = &imageFailingBinding{ResourceCatalog: imageSvc.catalog, fail: true}
	delivery, err = svc.SaveChart(ctx, chartSaveFixture())
	require.Nil(t, delivery)
	require.ErrorContains(t, err, "could not be attached")
	var count int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Count(&count).Error)
	require.Zero(t, count)
	svc.catalog = imageSvc.catalog
	delivery, err = svc.SaveChart(ctx, chartSaveFixture())
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Where("message_id = ? AND position = ?", "message", delivery.Index).Update("deleted_at", time.Now()).Error)
	_, err = svc.SaveChart(ctx, chartSaveFixture())
	require.ErrorContains(t, err, "no longer available")
}
