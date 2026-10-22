package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func chartPNGFixture(width, height int) []byte {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

func chartSaveFixture() chart.SaveRequest {
	return chart.SaveRequest{SessionID: "session", MessageID: "message", ToolCallID: "chart-call", SourceID: "actual-table", FileName: "../../ignored.html", PNG: chartPNGFixture(32, 24)}
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
	require.Equal(t, "chart-"+chartDigest(request.PNG)[:16]+".png", delivery.FileName)
	stream, err := svc.files.GetFile(ctx, delivery.Handle)
	require.NoError(t, err)
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, request.PNG, data)
	config, err := png.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, 32, config.Width)
	require.Equal(t, 24, config.Height)
	var saved types.MessageArtifactRecord
	require.NoError(t, db.Where("url = ?", delivery.Handle).First(&saved).Error)
	require.Equal(t, ".png", saved.FileType)
	require.EqualValues(t, len(data), saved.FileSize)
	require.Equal(t, chartDigest(data), saved.ContentHash)
	handle, ok := types.ParseResourcePath(delivery.Handle)
	require.True(t, ok)
	var resource types.StoredResource
	require.NoError(t, db.Where("handle = ?", handle).First(&resource).Error)
	require.Equal(t, "image/png", resource.MimeType)
	require.Equal(t, "image", resource.Kind)
	require.Equal(t, saved.ContentHash, resource.ContentHash)
	var binding types.ResourceBinding
	require.NoError(t, db.Where("owner_id = ? AND relation = ?", "message", types.ResourceRelationArtifact).First(&binding).Error)
	require.Equal(t, uint64(7), binding.TenantID)
	replayed, err := svc.SaveChart(ctx, request)
	require.NoError(t, err)
	require.Equal(t, delivery, replayed)
	var count int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	request.PNG = chartPNGFixture(33, 24)
	_, err = svc.SaveChart(ctx, request)
	require.ErrorContains(t, err, "parameters changed")
	var unchanged types.MessageArtifactRecord
	require.NoError(t, db.First(&unchanged, "id = ?", prior.ID).Error)
	require.Equal(t, prior.URL, unchanged.URL)
}

func TestChartArtifactsRejectInvalidPNGWithoutDeliveringFallback(t *testing.T) {
	imageSvc, db, ctx := newImageDeliveryFixture(t, &imageGeneratorFixture{})
	svc := NewChartArtifactService(db, imageSvc.sessions, imageSvc.files, imageSvc.catalog)
	var jpegBytes bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
	// A CRC-valid IHDR tests dimension rejection before a large allocation.
	oversizeHeader := func(width, height uint32) []byte {
		data := chartPNGFixture(1, 1)
		binary.BigEndian.PutUint32(data[16:20], width)
		binary.BigEndian.PutUint32(data[20:24], height)
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		return data
	}
	valid := chartPNGFixture(2, 2)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"html", []byte("<!doctype html><html>legacy chart</html>")},
		{"jpeg", jpegBytes.Bytes()},
		{"byte-limit", make([]byte, chart.MaxPNGBytes+1)},
		{"wide", oversizeHeader(12001, 1)},
		{"pixel-limit", oversizeHeader(10000, 10000)},
		{"zero-dimension", oversizeHeader(0, 1)},
		{"truncated", valid[:len(valid)-12]},
		{"corrupt-pixels", append(append([]byte{}, valid[:33]...), []byte("broken image data")...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := chartSaveFixture()
			request.PNG = test.data
			delivery, err := svc.SaveChart(ctx, request)
			require.Nil(t, delivery)
			require.ErrorContains(t, err, "PNG")
		})
	}
	var count int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&types.StoredResource{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestChartArtifactsPreserveLegacyHTMLHistory(t *testing.T) {
	imageSvc, db, ctx := newImageDeliveryFixture(t, &imageGeneratorFixture{})
	svc := NewChartArtifactService(db, imageSvc.sessions, imageSvc.files, imageSvc.catalog)
	legacyBytes := []byte("<!doctype html><html><body>Historical chart</body></html>")
	legacyHandle, err := svc.files.SaveBytes(ctx, legacyBytes, 7, "historical-chart.html", false)
	require.NoError(t, err)
	require.NoError(t, svc.catalog.Bind(ctx, legacyHandle, types.ResourceOwnerMessage, "message", types.ResourceRelationArtifact))
	identity, err := json.Marshal([]any{uint64(7), "session", "message", "chart-call"})
	require.NoError(t, err)
	legacy := types.NewMessageArtifactRecords("session", "message", types.MessageArtifacts{{URL: legacyHandle, FileName: "historical-chart.html", FileType: ".html", ContentHash: chartDigest(legacyBytes), SourcePath: "data-chart://" + chartDigest(identity)}})[0]
	require.NoError(t, db.Create(&legacy).Error)
	delivery, err := svc.SaveChart(ctx, chartSaveFixture())
	require.Nil(t, delivery)
	require.ErrorContains(t, err, "parameters changed")
	request := chartSaveFixture()
	request.ToolCallID = "new-png-execution"
	delivery, err = svc.SaveChart(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 1, delivery.Index)
	var unchanged types.MessageArtifactRecord
	require.NoError(t, db.First(&unchanged, "id = ?", legacy.ID).Error)
	require.Equal(t, legacy.URL, unchanged.URL)
	require.Equal(t, ".html", unchanged.FileType)
	require.Equal(t, legacy.ContentHash, unchanged.ContentHash)
	require.Equal(t, legacy.SourcePath, unchanged.SourcePath)
	require.Nil(t, unchanged.DeletedAt)
	stream, err := svc.files.GetFile(ctx, legacyHandle)
	require.NoError(t, err)
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, legacyBytes, data)
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
