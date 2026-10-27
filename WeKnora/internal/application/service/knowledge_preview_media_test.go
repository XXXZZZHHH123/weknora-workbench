package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type previewMediaKB struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *previewMediaKB) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

type previewMediaRepo struct {
	interfaces.KnowledgeRepository
	knowledge *types.Knowledge
	err       error
	calls     int
	second    *types.Knowledge
}

func (r *previewMediaRepo) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	r.calls++
	if r.calls > 1 && r.second != nil {
		return r.second, r.err
	}
	return r.knowledge, r.err
}

type previewMediaFiles struct {
	interfaces.FileService
	body  []byte
	reads int
}

func (f *previewMediaFiles) GetFile(context.Context, string) (io.ReadCloser, error) {
	f.reads++
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

type previewMediaReader struct {
	interfaces.DocumentReader
	result  *types.ReadResult
	request *types.ReadRequest
	err     error
}

func previewMediaContext() context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
}

func (r *previewMediaReader) Read(_ context.Context, request *types.ReadRequest) (*types.ReadResult, error) {
	r.request = request
	return r.result, r.err
}

func TestRenderKnowledgePreviewMediaBindsSourceAndAllowsReadableParseStates(t *testing.T) {
	var rendered bytes.Buffer
	pixels := image.NewRGBA(image.Rect(0, 0, 2, 2))
	pixels.Set(0, 0, color.RGBA{R: 20, G: 40, B: 60, A: 255})
	require.NoError(t, png.Encode(&rendered, pixels))
	source := []byte("stored original PPTX bytes")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	pngSHA := sha256.Sum256(rendered.Bytes())
	metadata := map[string]string{
		"evidence_version": "source_media_png_v1", "source_file_sha256": hex.EncodeToString(fileSHA[:]),
		"source_media_sha256": hex.EncodeToString(fileSHA[:]), "source_page": "3", "source_part": "ppt/media/member.wmf",
		"rendered_png_sha256": hex.EncodeToString(pngSHA[:]), "rendered_png_base64": base64.StdEncoding.EncodeToString(rendered.Bytes()),
	}
	for _, state := range []struct{ enable, parse string }{{"disabled", types.ParseStatusFailed}, {"enabled", types.ParseStatusCompleted}} {
		knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "original.pptx", FileType: "pptx",
			FilePath: "local://7/original.pptx", FileHash: hex.EncodeToString(md5Hash[:]), EnableStatus: state.enable, ParseStatus: state.parse}
		files := &previewMediaFiles{body: source}
		reader := &previewMediaReader{result: &types.ReadResult{MarkdownContent: "rendered source media", Metadata: metadata}}
		svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: files, documentReader: reader}
		got, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 3, "ppt/media/member.wmf", hex.EncodeToString(fileSHA[:]))
		require.NoError(t, err)
		require.Equal(t, rendered.Bytes(), got)
		require.Equal(t, "source_media_png_v1", reader.request.ParserEngine)
		require.Equal(t, "3", reader.request.ParserEngineOverrides["source_page"])
		require.Equal(t, "ppt/media/member.wmf", reader.request.ParserEngineOverrides["source_part"])
		_, err = svc.renderKnowledgeSourceMedia(previewMediaContext(), knowledge, 3, "ppt/media/member.wmf", hex.EncodeToString(fileSHA[:]), strings.Repeat("0", 64))
		require.ErrorContains(t, err, "stored vector does not match")
	}
}

func TestRenderKnowledgePreviewMediaRejectsDeletedOrChangedKnowledge(t *testing.T) {
	source := []byte("original")
	fileHash := md5.Sum(source)
	for _, variant := range []string{"deleted", "deleting", "changed"} {
		t.Run(variant, func(t *testing.T) {
			knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(fileHash[:])}
			current := *knowledge
			switch variant {
			case "deleted":
				current.DeletedAt.Valid = true
			case "deleting":
				current.ParseStatus = types.ParseStatusDeleting
			case "changed":
				current.FileHash = "different"
			}
			files := &previewMediaFiles{body: source}
			reader := &previewMediaReader{}
			svc := &knowledgeService{repo: &previewMediaRepo{knowledge: &current}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: files, documentReader: reader}
			fileSHA := sha256.Sum256(source)
			_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
			require.Error(t, err)
			require.Nil(t, reader.request)
		})
	}
}

func TestRenderKnowledgePreviewMediaRechecksKnowledgeBeforeReturning(t *testing.T) {
	var rendered bytes.Buffer
	pixels := image.NewRGBA(image.Rect(0, 0, 1, 1))
	require.NoError(t, png.Encode(&rendered, pixels))
	source := []byte("original")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	pngSHA := sha256.Sum256(rendered.Bytes())
	knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
	deleted := *knowledge
	deleted.DeletedAt.Valid = true
	repo := &previewMediaRepo{knowledge: knowledge, second: &deleted}
	reader := &previewMediaReader{result: &types.ReadResult{MarkdownContent: "rendered", Metadata: map[string]string{
		"evidence_version": "source_media_png_v1", "source_file_sha256": hex.EncodeToString(fileSHA[:]),
		"source_media_sha256": hex.EncodeToString(fileSHA[:]), "source_page": "1", "source_part": "ppt/media/a.emf",
		"rendered_png_sha256": hex.EncodeToString(pngSHA[:]), "rendered_png_base64": base64.StdEncoding.EncodeToString(rendered.Bytes()),
	}}}
	svc := &knowledgeService{repo: repo, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: &previewMediaFiles{body: source}, documentReader: reader}
	_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
	require.Error(t, err)
	require.Equal(t, 2, repo.calls)
}

func TestRenderKnowledgePreviewMediaRejectsInvalidBindingAndPNG(t *testing.T) {
	source := []byte("original")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	for _, variant := range []string{"wrong-part", "bad-png", "reader-error"} {
		t.Run(variant, func(t *testing.T) {
			meta := map[string]string{"evidence_version": "source_media_png_v1", "source_file_sha256": hex.EncodeToString(fileSHA[:]),
				"source_media_sha256": hex.EncodeToString(fileSHA[:]), "source_page": "1", "source_part": "ppt/media/a.emf",
				"rendered_png_sha256": hex.EncodeToString(fileSHA[:]), "rendered_png_base64": base64.StdEncoding.EncodeToString([]byte("not png"))}
			if variant == "wrong-part" {
				meta["source_part"] = "ppt/media/other.emf"
			}
			result := &types.ReadResult{MarkdownContent: "nonempty", Metadata: meta}
			if variant == "reader-error" {
				result.Error = "failed"
			}
			knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
			svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: &previewMediaFiles{body: source}, documentReader: &previewMediaReader{result: result}}
			_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
			require.Error(t, err)
		})
	}
}

func TestRenderKnowledgePreviewMediaRequiresMatchingSourceSHAAndTenantContext(t *testing.T) {
	source := []byte("current original")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		sha  string
	}{
		{name: "missing hash", ctx: previewMediaContext()},
		{name: "wrong hash", ctx: previewMediaContext(), sha: strings.Repeat("0", 64)},
		{name: "missing tenant", ctx: context.Background(), sha: hex.EncodeToString(fileSHA[:])},
		{name: "wrong tenant", ctx: context.WithValue(context.Background(), types.TenantIDContextKey, uint64(8)), sha: hex.EncodeToString(fileSHA[:])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := &previewMediaFiles{body: source}
			reader := &previewMediaReader{}
			svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: files, documentReader: reader}
			_, err := svc.RenderKnowledgePreviewMedia(tc.ctx, knowledge, 1, "ppt/media/a.emf", tc.sha)
			require.Error(t, err)
			require.Nil(t, reader.request)
			if tc.name == "missing hash" || tc.name == "missing tenant" || tc.name == "wrong tenant" {
				require.Zero(t, files.reads)
			}
			if tc.name == "wrong hash" {
				require.Equal(t, 1, files.reads)
				appErr, ok := apperrors.IsAppError(err)
				require.True(t, ok)
				require.Equal(t, 409, appErr.HTTPCode)
			}
			if tc.name == "missing hash" {
				appErr, ok := apperrors.IsAppError(err)
				require.True(t, ok)
				require.Equal(t, 400, appErr.HTTPCode)
			}
		})
	}
}

func TestRenderKnowledgePreviewMediaMapsOnlyTypedReaderFailures(t *testing.T) {
	source := []byte("original")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	for _, tc := range []struct {
		code     string
		httpCode int
	}{{"invalid_selector", 400}, {"busy", 503}, {"timeout", 504}, {"cancelled", 0}, {"render_failed", 0}, {"", 0}} {
		knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
		result := &types.ReadResult{Error: "same opaque reader error"}
		result.Metadata = map[string]string{"evidence_version": "source_media_png_v1", "source_media_error_code": tc.code}
		svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: &previewMediaFiles{body: source}, documentReader: &previewMediaReader{result: result}}
		_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
		require.Error(t, err)
		appErr, ok := apperrors.IsAppError(err)
		require.Equal(t, tc.httpCode != 0, ok, "error wording cannot determine its HTTP category")
		if tc.httpCode != 0 {
			require.Equal(t, tc.httpCode, appErr.HTTPCode)
		}
		if tc.code == "cancelled" {
			require.ErrorIs(t, err, context.Canceled)
		}
	}
}

func TestRenderKnowledgePreviewMediaPropagatesKnowledgeLookupFailure(t *testing.T) {
	source := []byte("original")
	md5Hash := md5.Sum(source)
	knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
	svc := &knowledgeService{repo: &previewMediaRepo{err: errors.New("missing")}, documentReader: &previewMediaReader{}}
	fileSHA := sha256.Sum256(source)
	_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
	require.Error(t, err)
}

func TestRenderKnowledgePreviewMediaKeepsCallerCancellation(t *testing.T) {
	source := []byte("original")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(md5Hash[:])}
	svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: &previewMediaFiles{body: source}, documentReader: &previewMediaReader{err: errors.New("wrapped RPC cancellation")}}
	ctx, cancel := context.WithCancel(previewMediaContext())
	cancel()
	_, err := svc.RenderKnowledgePreviewMedia(ctx, knowledge, 1, "ppt/media/a.emf", hex.EncodeToString(fileSHA[:]))
	require.ErrorIs(t, err, context.Canceled)
}

type previewMediaChunks struct {
	interfaces.ChunkRepository
	refs []string
	args []any
}

func (r *previewMediaChunks) FindDocumentImageSourceReferences(
	_ context.Context, tenantID uint64, knowledgeID string, page int, part, hash string,
) ([]string, error) {
	r.args = []any{tenantID, knowledgeID, page, part, hash}
	return r.refs, nil
}

type previewMediaCatalog struct {
	interfaces.ResourceCatalog
	resource *types.StoredResource
}

func (c *previewMediaCatalog) Resolve(context.Context, string) (*types.StoredResource, error) {
	return c.resource, nil
}

type previewMediaPathFiles struct {
	interfaces.FileService
	bodies map[string][]byte
	reads  []string
}

func (f *previewMediaPathFiles) GetFile(_ context.Context, path string) (io.ReadCloser, error) {
	f.reads = append(f.reads, path)
	body, ok := f.bodies[path]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func TestRenderKnowledgePreviewMediaServesParseTimePNGWithoutReader(t *testing.T) {
	var stored bytes.Buffer
	require.NoError(t, png.Encode(&stored, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	source := []byte("stored original PPTX with a parse-time chart")
	md5Hash := md5.Sum(source)
	fileSHA := sha256.Sum256(source)
	knowledge := &types.Knowledge{ID: "doc-stored", TenantID: 7, KnowledgeBaseID: "kb", FileName: "weekly.pptx", FileType: "pptx",
		FilePath: "local://7/weekly.pptx", FileHash: hex.EncodeToString(md5Hash[:]), ParseStatus: types.ParseStatusCompleted}
	newService := func(resource *types.StoredResource) (*knowledgeService, *previewMediaPathFiles, *previewMediaReader, *previewMediaChunks) {
		files := &previewMediaPathFiles{bodies: map[string][]byte{knowledge.FilePath: source, "resource://chart": stored.Bytes()}}
		reader := &previewMediaReader{err: errors.New("reader must not run")}
		chunks := &previewMediaChunks{refs: []string{"resource://chart"}}
		return &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, chunkRepo: chunks,
			resourceCatalog: &previewMediaCatalog{resource: resource}, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}},
			fileSvc: files, documentReader: reader}, files, reader, chunks
	}
	png := &types.StoredResource{TenantID: 7, State: types.ResourceStateActive, MimeType: "image/png"}

	svc, files, reader, chunks := newService(png)
	for range 2 {
		got, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 14, "ppt/media/image107.emf", hex.EncodeToString(fileSHA[:]))
		require.NoError(t, err)
		require.Equal(t, stored.Bytes(), got)
	}
	require.Nil(t, reader.request)
	require.Equal(t, []any{uint64(7), "doc-stored", 14, "ppt/media/image107.emf", knowledge.FileHash}, chunks.args)
	// The original is hashed once; later parts reuse the remembered digest.
	require.Equal(t, []string{knowledge.FilePath, "resource://chart", "resource://chart"}, files.reads)

	// A preview of different bytes never receives the stored PNG.
	_, err := svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 14, "ppt/media/image107.emf", strings.Repeat("a", 64))
	require.Error(t, err)

	// A part the parse budget left as a raw vector still renders on demand.
	svc, _, reader, _ = newService(&types.StoredResource{TenantID: 7, State: types.ResourceStateActive, MimeType: "image/emf"})
	_, err = svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 14, "ppt/media/image107.emf", hex.EncodeToString(fileSHA[:]))
	require.ErrorContains(t, err, "reader must not run")
	require.NotNil(t, reader.request)

	// Resources from another workspace are never served.
	svc, _, reader, _ = newService(&types.StoredResource{TenantID: 8, State: types.ResourceStateActive, MimeType: "image/png"})
	_, err = svc.RenderKnowledgePreviewMedia(previewMediaContext(), knowledge, 14, "ppt/media/image107.emf", hex.EncodeToString(fileSHA[:]))
	require.Error(t, err)
	require.NotNil(t, reader.request)
}
