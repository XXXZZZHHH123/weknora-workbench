package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const originalMediaXMLLimit = 4 << 20
const originalMediaMemberLimit = 16 << 20
const originalMediaPageLimit = 128
const originalMediaCatalogLimit = 32
const originalDrawingNS = "http://schemas.openxmlformats.org/drawingml/2006/main"
const originalPresentationNS = "http://schemas.openxmlformats.org/presentationml/2006/main"
const originalRelationshipNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

type originalMediaArchive struct {
	files map[string]*zip.File
	ctx   context.Context
}
type originalMediaPlacement struct{ Part, Label string }

// The tool resolves KB read permissions and the source/page constraint before
// this callback. Recheck the current document and storage KB on each invocation;
// selectors never expand that authorized scope and never create stored assets.
func (s *knowledgeService) loadOriginalMediaSource(ctx context.Context, k *types.Knowledge) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if k == nil || k.ID == "" || k.KnowledgeBaseID == "" || k.TenantID == 0 || s.kbService == nil || !s.figureSourceCurrent(ctx, k) {
		return nil, "", errors.New("original media source is stale or unavailable")
	}
	kb, e := s.kbService.GetKnowledgeBaseByID(ctx, k.KnowledgeBaseID)
	if e != nil || kb == nil || kb.ID != k.KnowledgeBaseID || kb.TenantID != k.TenantID {
		return nil, "", errors.New("original media KB scope mismatch")
	}
	files := s.resolveFileServiceForPath(ctx, kb, k.FilePath)
	if files == nil {
		return nil, "", errors.New("original media storage unavailable")
	}
	rd, e := files.GetFile(ctx, k.FilePath)
	if e != nil {
		return nil, "", e
	}
	defer rd.Close()
	content, e := io.ReadAll(io.LimitReader(originalMediaContextReader{ctx, rd}, previewMediaSourceLimit+1))
	if e != nil || len(content) == 0 || len(content) > previewMediaSourceLimit {
		return nil, "", errors.New("original media source exceeds read limit")
	}
	sha := sha256.Sum256(content)
	hash := hex.EncodeToString(sha[:])
	md := md5.Sum(content)
	if !strings.EqualFold(k.FileHash, hash) && !strings.EqualFold(k.FileHash, hex.EncodeToString(md[:])) {
		return nil, "", errors.New("original media upload fingerprint mismatch")
	}
	if !s.figureSourceCurrent(ctx, k) {
		return nil, "", errors.New("original media source changed during read")
	}
	return content, hash, nil
}

func openOriginalMediaArchive(content []byte) (*originalMediaArchive, error) {
	z, e := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if e != nil {
		return nil, e
	}
	if len(z.File) > 20000 {
		return nil, errors.New("too many original ZIP members")
	}
	a := &originalMediaArchive{files: map[string]*zip.File{}}
	for _, f := range z.File {
		if _, exists := a.files[f.Name]; exists {
			return nil, errors.New("duplicate original ZIP member")
		}
		a.files[f.Name] = f
	}
	return a, nil
}
func (a *originalMediaArchive) read(name string, limit int64) ([]byte, error) {
	f := a.files[name]
	if f == nil || f.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("missing or oversized original member")
	}
	r, e := f.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(originalMediaContextReader{a.ctx, r}, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("original member read failed")
	}
	return b, nil
}
func (a *originalMediaArchive) relationships(part string) (map[string]string, error) {
	rel := path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
	if a.files[rel] == nil {
		return map[string]string{}, nil
	}
	b, e := a.read(rel, originalMediaXMLLimit)
	if e != nil {
		return nil, e
	}
	var doc struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if e = xml.Unmarshal(b, &doc); e != nil {
		return nil, e
	}
	out := map[string]string{}
	seen := map[string]bool{}
	for _, r := range doc.Items {
		if r.ID == "" || seen[r.ID] {
			return nil, errors.New("ambiguous original relationship")
		}
		seen[r.ID] = true
		if strings.EqualFold(r.Mode, "External") {
			continue
		}
		if r.Target == "" || strings.ContainsAny(r.Target, ":\\?#") {
			return nil, errors.New("invalid original relationship target")
		}
		target := path.Clean(path.Join(path.Dir(part), r.Target))
		if strings.HasPrefix(r.Target, "/") {
			target = path.Clean(strings.TrimPrefix(r.Target, "/"))
		}
		if strings.HasPrefix(target, "../") || strings.HasPrefix(target, "/") {
			return nil, errors.New("original relationship escapes package")
		}
		if a.files[target] == nil {
			return nil, errors.New("original relationship member missing")
		}
		out[r.ID] = target
	}
	return out, nil
}
func xmlAttribute(attrs []xml.Attr, space, local string) string {
	for _, a := range attrs {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
func (a *originalMediaArchive) slides() ([]string, error) {
	b, e := a.read("ppt/presentation.xml", originalMediaXMLLimit)
	if e != nil {
		return nil, e
	}
	rels, e := a.relationships("ppt/presentation.xml")
	if e != nil {
		return nil, e
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var slides []string
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		v, ok := t.(xml.StartElement)
		if !ok || v.Name.Space != originalPresentationNS || v.Name.Local != "sldId" {
			continue
		}
		p := rels[xmlAttribute(v.Attr, originalRelationshipNS, "id")]
		if !strings.HasPrefix(p, "ppt/slides/") {
			return nil, errors.New("invalid original slide relation")
		}
		slides = append(slides, p)
		if len(slides) > 1000 {
			return nil, errors.New("too many original slides")
		}
	}
	return slides, nil
}
func (a *originalMediaArchive) pictures(slide string) ([]originalMediaPlacement, error) {
	b, e := a.read(slide, originalMediaXMLLimit)
	if e != nil {
		return nil, e
	}
	rels, e := a.relationships(slide)
	if e != nil {
		return nil, e
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var out []originalMediaPlacement
	seen := map[string]bool{}
	depth := 0
	picDepth := 0
	label := ""
	var targets []string
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch v := t.(type) {
		case xml.StartElement:
			depth++
			if v.Name.Space == originalPresentationNS && v.Name.Local == "pic" {
				picDepth = depth
				label = ""
				targets = nil
			}
			if picDepth == 0 {
				continue
			}
			if v.Name.Space == originalPresentationNS && v.Name.Local == "cNvPr" {
				label = xmlAttribute(v.Attr, "", "descr")
				if label == "" {
					label = xmlAttribute(v.Attr, "", "name")
				}
			}
			if v.Name.Space == originalDrawingNS && v.Name.Local == "blip" {
				target := rels[xmlAttribute(v.Attr, originalRelationshipNS, "embed")]
				if strings.HasPrefix(target, "ppt/media/") && path.Dir(target) == "ppt/media" {
					targets = append(targets, target)
				}
			}
		case xml.EndElement:
			if depth == picDepth {
				for _, target := range targets {
					if !seen[target] {
						seen[target] = true
						out = append(out, originalMediaPlacement{target, label})
						if len(out) > originalMediaPageLimit {
							return nil, errors.New("too many original images on page")
						}
					}
				}
				picDepth = 0
			}
			depth--
		}
	}
	return out, nil
}

func (s *knowledgeService) ListDocumentOriginalImages(ctx context.Context, k *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
	if len(pages) == 0 || len(pages) > 8 {
		return nil, errors.New("original image catalog requires one to eight physical pages")
	}
	content, hash, e := s.loadOriginalMediaSource(ctx, k)
	if e != nil {
		return nil, e
	}
	a, e := openOriginalMediaArchive(content)
	if e != nil {
		return nil, e
	}
	a.ctx = ctx
	slides, e := a.slides()
	if e != nil {
		return nil, e
	}
	out := []types.OriginalDocumentImage{}
	placements := map[int][]originalMediaPlacement{}
	count := 0
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page < 1 || page > len(slides) {
			return nil, errors.New("original image page out of range")
		}
		if _, ok := placements[page]; ok {
			continue
		}
		pics, err := a.pictures(slides[page-1])
		if err != nil {
			return nil, err
		}
		placements[page] = pics
		count += len(pics)
	}
	if count > originalMediaCatalogLimit {
		return nil, &types.OriginalMediaCatalogLimitError{Total: count, Limit: originalMediaCatalogLimit}
	}
	seen := map[int]bool{}
	total := 0
	for _, page := range pages {
		if page < 1 || page > len(slides) {
			return nil, errors.New("original image page out of range")
		}
		if seen[page] {
			continue
		}
		seen[page] = true
		pics := placements[page]
		for _, p := range pics {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			remaining := (64 << 20) - total
			if remaining <= 0 {
				return nil, errors.New("original catalog decompression budget exceeded")
			}
			raw, e := a.read(p.Part, int64(min(originalMediaMemberLimit, remaining)))
			if e != nil {
				return nil, e
			}
			total += len(raw)
			if total > 64<<20 {
				return nil, errors.New("original catalog decompression budget exceeded")
			}
			digest := sha256.Sum256(raw)
			ref := types.OriginalMediaReference{KnowledgeID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, SourceSHA256: hash, Page: page, Part: p.Part, MediaSHA256: hex.EncodeToString(digest[:])}
			label := []rune(p.Label)
			if len(label) > 200 {
				label = label[:200]
			}
			out = append(out, types.OriginalDocumentImage{Reference: ref.Reference(), Page: page, Part: p.Part, Label: string(label), LabelTruncated: len([]rune(p.Label)) > 200})
		}
	}
	if !s.figureSourceCurrent(ctx, k) {
		return nil, errors.New("original source changed during catalog")
	}
	return out, nil
}

func (s *knowledgeService) readOriginalMediaSelection(ctx context.Context, k *types.Knowledge, ref string, page int, part string) ([]byte, string, error) {
	sel, e := types.ParseOriginalMediaReference(ref)
	if e != nil || k == nil || sel.KnowledgeID != k.ID || sel.KnowledgeBaseID != k.KnowledgeBaseID || sel.Page != page || sel.Part != part {
		return nil, "", errors.New("original image selector does not match authorized scope")
	}
	content, hash, e := s.loadOriginalMediaSource(ctx, k)
	if e != nil {
		return nil, "", e
	}
	if hash != sel.SourceSHA256 {
		return nil, "", errors.New("original image selector is stale")
	}
	a, e := openOriginalMediaArchive(content)
	if e != nil {
		return nil, "", e
	}
	a.ctx = ctx
	slides, e := a.slides()
	if e != nil || page < 1 || page > len(slides) {
		return nil, "", errors.New("original image page unavailable")
	}
	pics, e := a.pictures(slides[page-1])
	if e != nil {
		return nil, "", e
	}
	if !slices.ContainsFunc(pics, func(p originalMediaPlacement) bool { return p.Part == part }) {
		return nil, "", errors.New("original image is not related to page")
	}
	raw, e := a.read(part, originalMediaMemberLimit)
	if e != nil {
		return nil, "", e
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != sel.MediaSHA256 {
		return nil, "", fmt.Errorf("original image bytes differ from selector")
	}
	if !s.figureSourceCurrent(ctx, k) {
		return nil, "", errors.New("original source changed during media read")
	}
	return raw, hash, nil
}

// Stop ZIP and storage reads promptly when the agent request is cancelled.
type originalMediaContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r originalMediaContextReader) Read(p []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(p)
}
