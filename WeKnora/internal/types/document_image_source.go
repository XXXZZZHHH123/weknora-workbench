package types

import "encoding/json"

// DocumentImageSourcesKey stores parser-derived navigation, not observations.
// It is independent of OCR and markdown splitting. The containing chunk supplies
// document/tenant scope; SourceHash identifies that document's source revision.
const DocumentImageSourcesKey = "document_image_sources_v1"

type DocumentImageSource struct {
	Reference  string `json:"reference"`
	Page       int    `json:"page"`
	SourcePart string `json:"source_part"`
	SourceHash string `json:"source_hash"`
}

func ChunkDocumentImageSources(metadata JSON) []DocumentImageSource {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(metadata, &envelope) != nil {
		return nil
	}
	var sources []DocumentImageSource
	if json.Unmarshal(envelope[DocumentImageSourcesKey], &sources) != nil {
		return nil
	}
	return sources
}
