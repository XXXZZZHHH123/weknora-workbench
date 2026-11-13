package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func originalMediaTestDeck(t *testing.T, change func(map[string]string)) []byte {
	t.Helper()
	parts := map[string]string{
		"ppt/presentation.xml":             fmt.Sprintf(`<p:presentation xmlns:p="%s" xmlns:r="%s"><p:sldIdLst><p:sldId r:id="s1"/><p:sldId r:id="s2"/></p:sldIdLst></p:presentation>`, originalPresentationNS, originalRelationshipNS),
		"ppt/_rels/presentation.xml.rels":  `<Relationships><Relationship Id="s1" Target="slides/slide9.xml"/><Relationship Id="s2" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide9.xml":            fmt.Sprintf(`<p:sld xmlns:p="%s" xmlns:a="%s" xmlns:r="%s"><p:cSld><p:spTree><p:pic><p:nvPicPr><p:cNvPr name="Decoration"/></p:nvPicPr><p:blipFill><a:blip r:embed="r1"/></p:blipFill></p:pic><p:grpSp><p:pic><p:nvPicPr><p:cNvPr name="Original figure"/></p:nvPicPr><p:blipFill><a:blip r:embed="r2"/></p:blipFill></p:pic></p:grpSp></p:spTree></p:cSld></p:sld>`, originalPresentationNS, originalDrawingNS, originalRelationshipNS),
		"ppt/slides/_rels/slide9.xml.rels": `<Relationships><Relationship Id="r1" Target="../media/icon.png"/><Relationship Id="r2" Target="../media/plot.emf"/></Relationships>`,
		"ppt/slides/slide2.xml":            fmt.Sprintf(`<p:sld xmlns:p="%s"/>`, originalPresentationNS),
		"ppt/media/icon.png":               "png fixture", "ppt/media/plot.emf": "bound original",
	}
	if change != nil {
		change(parts)
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, body := range parts {
		f, e := z.Create(name)
		require.NoError(t, e)
		_, e = f.Write([]byte(body))
		require.NoError(t, e)
	}
	require.NoError(t, z.Close())
	return out.Bytes()
}
func originalMediaTestService(t *testing.T, content []byte) (*knowledgeService, *types.Knowledge, *previewMediaFiles, *previewMediaRepo) {
	hash := md5.Sum(content)
	k := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileType: "pptx", FileName: "original.pptx", FilePath: "local://7/original.pptx", FileHash: hex.EncodeToString(hash[:])}
	repo := &previewMediaRepo{knowledge: k}
	files := &previewMediaFiles{body: content}
	return &knowledgeService{repo: repo, kbService: &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}, fileSvc: files}, k, files, repo
}
func TestOriginalMediaCatalogUsesPhysicalRelationsWithoutWritingOrRendering(t *testing.T) {
	source := originalMediaTestDeck(t, nil)
	s, k, files, _ := originalMediaTestService(t, source)
	before := *k
	images, e := s.ListDocumentOriginalImages(previewMediaContext(), k, []int{1, 2})
	require.NoError(t, e)
	require.Len(t, images, 2)
	require.Equal(t, "ppt/media/icon.png", images[0].Part)
	require.Equal(t, "ppt/media/plot.emf", images[1].Part)
	require.Equal(t, 1, images[1].Page)
	sel, e := types.ParseOriginalMediaReference(images[1].Reference)
	require.NoError(t, e)
	require.Equal(t, "doc", sel.KnowledgeID)
	raw, _, e := s.readOriginalMediaSelection(previewMediaContext(), k, images[1].Reference, 1, images[1].Part)
	require.NoError(t, e)
	require.Equal(t, []byte("bound original"), raw)
	require.Equal(t, before, *k)
	require.Equal(t, source, files.body)
	require.Nil(t, s.documentReader, "catalog must not convert")
	require.Nil(t, s.chunkRepo, "historical chunks are never mutated")
}
func TestOriginalMediaSelectorRejectsWrongScopeAndStaleSources(t *testing.T) {
	for _, variant := range []string{"page", "part", "knowledge", "kb", "sha", "media", "forged", "replaced", "deleted", "tenant", "changed-bytes", "changed-during-read"} {
		t.Run(variant, func(t *testing.T) {
			source := originalMediaTestDeck(t, nil)
			s, k, files, repo := originalMediaTestService(t, source)
			items, e := s.ListDocumentOriginalImages(previewMediaContext(), k, []int{1})
			require.NoError(t, e)
			ref := items[1].Reference
			sel, e := types.ParseOriginalMediaReference(ref)
			require.NoError(t, e)
			page, part := 1, items[1].Part
			switch variant {
			case "page":
				page = 2
			case "part":
				part = "ppt/media/icon.png"
			case "knowledge":
				sel.KnowledgeID = "foreign"
			case "kb":
				sel.KnowledgeBaseID = "foreign"
			case "sha":
				sel.SourceSHA256 = strings.Repeat("0", 64)
			case "media":
				sel.MediaSHA256 = strings.Repeat("0", 64)
			case "forged":
				sel.Part = "ppt/media/missing.emf"
				part = sel.Part
			case "replaced":
				changed := *k
				changed.FileHash = strings.Repeat("0", 32)
				repo.knowledge = &changed
			case "deleted":
				changed := *k
				changed.ParseStatus = types.ParseStatusDeleting
				repo.knowledge = &changed
			case "tenant":
				s.kbService = &previewMediaKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 8}}
			case "changed-bytes":
				files.body = append(source, 1)
			case "changed-during-read":
				changed := *k
				changed.FilePath = "local://new.pptx"
				repo.calls = 0
				repo.second = &changed
			}
			ref = sel.Reference()
			_, _, e = s.readOriginalMediaSelection(previewMediaContext(), k, ref, page, part)
			require.Error(t, e)
		})
	}
}
func TestOriginalMediaCatalogRejectsAmbiguousAndExternalRelations(t *testing.T) {
	for _, variant := range []string{"duplicate-rel", "traversal", "external", "invalid-page"} {
		t.Run(variant, func(t *testing.T) {
			source := originalMediaTestDeck(t, func(p map[string]string) {
				switch variant {
				case "duplicate-rel":
					p["ppt/slides/_rels/slide9.xml.rels"] = `<Relationships><Relationship Id="r1" Target="../media/icon.png"/><Relationship Id="r1" Target="../media/plot.emf"/></Relationships>`
				case "traversal":
					p["ppt/slides/_rels/slide9.xml.rels"] = `<Relationships><Relationship Id="r1" Target="../../../etc/passwd"/></Relationships>`
				case "external":
					p["ppt/slides/_rels/slide9.xml.rels"] = `<Relationships><Relationship Id="r1" Target="https://example.invalid/a" TargetMode="External"/></Relationships>`
				}
			})
			s, k, _, _ := originalMediaTestService(t, source)
			page := 1
			if variant == "invalid-page" {
				page = 3
			}
			items, e := s.ListDocumentOriginalImages(previewMediaContext(), k, []int{page})
			if variant == "external" {
				require.NoError(t, e)
				require.Empty(t, items)
			} else {
				require.Error(t, e)
			}
		})
	}
}
func TestOriginalMediaPixelsAndGeometryKeepVersionAndMemberBinding(t *testing.T) {
	source := originalMediaTestDeck(t, nil)
	s, k, _, repo := originalMediaTestService(t, source)
	items, e := s.ListDocumentOriginalImages(previewMediaContext(), k, []int{1})
	require.NoError(t, e)
	item := items[1]
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	reader := &figureReader{media: []byte("bound original"), rendered: pngBytes.Bytes()}
	s.documentReader = reader
	got, e := s.ReadDocumentSourceImage(previewMediaContext(), k, item.Reference, 1, item.Part)
	require.NoError(t, e)
	require.Equal(t, pngBytes.Bytes(), got)
	figure, e := s.ReadDocumentFigureEvidence(previewMediaContext(), k, item.Reference, 1, item.Part, []string{"Alpha"})
	require.NoError(t, e)
	require.Equal(t, "source_figure_geometry_v1", figure["version"])
	reader.mutate = func(*types.ReadResult) {
		changed := *k
		changed.FileHash = strings.Repeat("0", 32)
		repo.knowledge = &changed
	}
	_, e = s.ReadDocumentFigureEvidence(context.Background(), k, item.Reference, 1, item.Part, []string{"Alpha"})
	require.Error(t, e)
}

func TestOriginalMediaCatalogLimitsLabelsCountsAndCancellation(t *testing.T) {
	source := originalMediaTestDeck(t, func(p map[string]string) {
		p["ppt/slides/slide9.xml"] = strings.Replace(p["ppt/slides/slide9.xml"], `name="Original figure"`, `descr="`+strings.Repeat("x", 5000)+`"`, 1)
	})
	s, k, _, _ := originalMediaTestService(t, source)
	items, err := s.ListDocumentOriginalImages(previewMediaContext(), k, []int{1})
	require.NoError(t, err)
	require.Len(t, []rune(items[1].Label), 200)
	require.True(t, items[1].LabelTruncated)
	many := originalMediaTestDeck(t, func(p map[string]string) {
		var pics, rels strings.Builder
		for i := 0; i < 33; i++ {
			fmt.Fprintf(&pics, `<p:pic><p:blipFill><a:blip r:embed="r%d"/></p:blipFill></p:pic>`, i)
			fmt.Fprintf(&rels, `<Relationship Id="r%d" Target="../media/f%d.emf"/>`, i, i)
			p[fmt.Sprintf("ppt/media/f%d.emf", i)] = "bytes"
		}
		p["ppt/slides/slide9.xml"] = fmt.Sprintf(`<p:sld xmlns:p="%s" xmlns:a="%s" xmlns:r="%s">%s</p:sld>`, originalPresentationNS, originalDrawingNS, originalRelationshipNS, pics.String())
		p["ppt/slides/_rels/slide9.xml.rels"] = "<Relationships>" + rels.String() + "</Relationships>"
	})
	s, k, files, _ := originalMediaTestService(t, many)
	_, err = s.ListDocumentOriginalImages(previewMediaContext(), k, []int{1})
	var limit *types.OriginalMediaCatalogLimitError
	require.ErrorAs(t, err, &limit)
	require.Equal(t, 33, limit.Total)
	require.Equal(t, 32, limit.Limit)
	ctx, cancel := context.WithCancel(previewMediaContext())
	cancel()
	reads := files.reads
	_, err = s.ListDocumentOriginalImages(ctx, k, []int{1})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, reads, files.reads)
}
func TestOriginalMediaArchiveRejectsDuplicateMembers(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := 0; i < 2; i++ {
		w, e := z.Create("ppt/media/a.emf")
		require.NoError(t, e)
		_, e = w.Write([]byte("a"))
		require.NoError(t, e)
	}
	require.NoError(t, z.Close())
	_, e := openOriginalMediaArchive(b.Bytes())
	require.ErrorContains(t, e, "duplicate")
}
