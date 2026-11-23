package types

import "encoding/json"

// DocumentImageSourcesKey stores parser-derived navigation, not observations.
// It is independent of OCR and markdown splitting. The containing chunk supplies
// document/tenant scope; SourceHash identifies that document's source revision.
const DocumentImageSourcesKey = "document_image_sources_v1"

const DocumentImageTextEvidenceKey = "image_text_evidence"

const DocumentImageTextEvidenceNotice = "Image OCR supplies a word-for-word transcription made at ingestion, and can contain recognition errors. " +
	"Image captions describe appearance and context. VLM observations are visual readings and may be estimates. " +
	"For exact values in table images, use the source-bound image_ocr transcription as primary evidence. Match the requested row and column using the actual headers, alignment and units. " +
	"If the OCR transcription and vision reading of the same cell conflict, recheck that location in the original image or explicitly report the unresolved conflict without silently choosing a reading. " +
	"A missing or unclear vision reading alone does not invalidate a source-bound transcription."

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
