package types

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOriginalMediaReferenceIsDistinctAndCanonical(t *testing.T) {
	s := OriginalMediaReference{KnowledgeID: "doc", KnowledgeBaseID: "kb", SourceSHA256: strings.Repeat("a", 64), Page: 2, Part: "ppt/media/x.emf", MediaSHA256: strings.Repeat("b", 64)}
	ref := s.Reference()
	parsed, err := ParseOriginalMediaReference(ref)
	require.NoError(t, err)
	require.Equal(t, s, parsed)
	_, resource := ParseResourcePath(ref)
	require.False(t, resource)
	for _, bad := range []string{"resource://AbCdEfGhIjKlMnOpQrStUv", ref + "=", OriginalMediaPrefix + "fake", ref + "\n", OriginalMediaPrefix + strings.Repeat("a", 4097)} {
		_, err = ParseOriginalMediaReference(bad)
		require.Error(t, err)
	}
}
