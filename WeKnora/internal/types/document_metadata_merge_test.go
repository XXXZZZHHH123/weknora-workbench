package types

import (
	"encoding/json"
	"testing"
)

func TestSetDocumentMetadataKeepsImageSourceBindings(t *testing.T) {
	chunk := &Chunk{Metadata: JSON(`{"document_image_sources_v1":[{"page":12,"reference":"resource://img","source_hash":"h","source_part":"ppt/media/image84.emf"}],"generated_questions":[{"id":"old","question":"旧问题"}]}`)}
	revision := 3
	if err := chunk.SetDocumentMetadata(&DocumentChunkMetadata{
		GeneratedQuestions:         []GeneratedQuestion{{ID: "q1", Question: "新问题", ContentRevision: &revision}},
		GeneratedQuestionsRevision: revision,
	}); err != nil {
		t.Fatal(err)
	}
	if sources := ChunkDocumentImageSources(chunk.Metadata); len(sources) != 1 || sources[0].SourcePart != "ppt/media/image84.emf" {
		t.Fatalf("image source binding lost: %s", chunk.Metadata)
	}
	meta, err := chunk.DocumentMetadata()
	if err != nil || len(meta.GeneratedQuestions) != 1 || meta.GeneratedQuestions[0].ID != "q1" || meta.GeneratedQuestionsRevision != 3 {
		t.Fatalf("questions not replaced: %s", chunk.Metadata)
	}

	// Removing the last question clears only the question keys.
	if err := chunk.SetDocumentMetadata(&DocumentChunkMetadata{}); err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(chunk.Metadata, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["generated_questions"]; ok || len(keys) != 1 || keys["document_image_sources_v1"] == nil {
		t.Fatalf("unexpected keys after clearing questions: %s", chunk.Metadata)
	}

	empty := &Chunk{}
	if err := empty.SetDocumentMetadata(nil); err != nil || empty.Metadata != nil {
		t.Fatalf("nil metadata on an empty chunk stays empty: %s", empty.Metadata)
	}
	invalid := &Chunk{Metadata: JSON(`[1,2]`)}
	if err := invalid.SetDocumentMetadata(&DocumentChunkMetadata{GeneratedQuestionsRevision: 1}); err != nil || string(invalid.Metadata) != `{"generated_questions_revision":1}` {
		t.Fatalf("non-object metadata is replaced as before: %s", invalid.Metadata)
	}
}
