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
	"image/png"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type deferredSourceKB struct {
	*previewMediaKB
	data []byte
	err  error
}

func (k *deferredSourceKB) ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error) {
	return k.data, k.err
}

func TestReadDocumentSourceImageVerifiesDeferredBytesAndKeepsOwnershipGuard(t *testing.T) {
	var rendered bytes.Buffer
	require.NoError(t, png.Encode(&rendered, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	original, vector := []byte("original presentation"), []byte("original vector")
	fileHash, sourceSHA, mediaSHA, pngSHA := md5.Sum(original), sha256.Sum256(original), sha256.Sum256(vector), sha256.Sum256(rendered.Bytes())
	knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileName: "a.pptx", FileType: "pptx", FilePath: "local://a.pptx", FileHash: hex.EncodeToString(fileHash[:])}
	kb := &deferredSourceKB{previewMediaKB: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, data: vector}
	files := &previewMediaFiles{body: original}
	reader := &previewMediaReader{result: &types.ReadResult{MarkdownContent: "rendered", Metadata: map[string]string{
		"evidence_version": "source_media_png_v1", "source_file_sha256": hex.EncodeToString(sourceSHA[:]),
		"source_media_sha256": hex.EncodeToString(mediaSHA[:]), "source_page": "2", "source_part": "ppt/media/a.emf",
		"rendered_png_sha256": hex.EncodeToString(pngSHA[:]), "rendered_png_base64": base64.StdEncoding.EncodeToString(rendered.Bytes()),
	}}}
	svc := &knowledgeService{repo: &previewMediaRepo{knowledge: knowledge}, kbService: kb, fileSvc: files, documentReader: reader}
	got, err := svc.ReadDocumentSourceImage(previewMediaContext(), knowledge, "resource://AbCdEfGhIjKlMnOpQrStUv", 2, "ppt/media/a.emf")
	require.NoError(t, err)
	require.Equal(t, rendered.Bytes(), got)
	kb.data = []byte("another vector")
	_, err = svc.ReadDocumentSourceImage(previewMediaContext(), knowledge, "resource://AbCdEfGhIjKlMnOpQrStUv", 2, "ppt/media/a.emf")
	require.ErrorContains(t, err, "stored vector does not match")
	reads := files.reads
	kb.err = errors.New("ownership denied")
	_, err = svc.ReadDocumentSourceImage(previewMediaContext(), knowledge, "resource://AbCdEfGhIjKlMnOpQrStUv", 2, "ppt/media/a.emf")
	require.ErrorContains(t, err, "ownership denied")
	require.Equal(t, reads, files.reads)
	kb.err = nil
	kb.data = rendered.Bytes()
	got, err = svc.ReadDocumentSourceImage(previewMediaContext(), knowledge, "resource://AbCdEfGhIjKlMnOpQrStUv", 2, "ppt/media/a.emf")
	require.NoError(t, err)
	require.Equal(t, rendered.Bytes(), got)
	require.Equal(t, reads, files.reads)
}
