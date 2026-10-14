package types

import "testing"

func TestExplicitIndexingOptOutSurvivesDefaultsAndSerialization(t *testing.T) {
	kb := KnowledgeBase{IndexingStrategy: IndexingStrategy{Disabled: true}}
	kb.EnsureDefaults()
	if kb.IndexingStrategy.NeedsEmbedding() || kb.IndexingStrategy.NeedsChunks() || kb.IndexingStrategy.HasAnyIndexing() {
		t.Fatal("source-only KB enabled indexing")
	}
	value, err := kb.IndexingStrategy.Value()
	if err != nil {
		t.Fatal(err)
	}
	var restored IndexingStrategy
	if err = restored.Scan(value); err != nil {
		t.Fatal(err)
	}
	kb.IndexingStrategy = restored
	kb.EnsureDefaults()
	if !kb.IndexingStrategy.Disabled || kb.IndexingStrategy.HasAnyIndexing() {
		t.Fatal(kb.IndexingStrategy)
	}
	kb.IndexingStrategy.VectorEnabled = true
	kb.EnsureDefaults()
	if kb.IndexingStrategy.Disabled || !kb.IndexingStrategy.NeedsEmbedding() {
		t.Fatal("explicit indexing selection did not resume original behavior")
	}
	ordinary := KnowledgeBase{}
	ordinary.EnsureDefaults()
	if ordinary.IndexingStrategy.Disabled || !ordinary.IndexingStrategy.VectorEnabled || !ordinary.IndexingStrategy.KeywordEnabled {
		t.Fatal("ordinary KB defaults changed")
	}
}
