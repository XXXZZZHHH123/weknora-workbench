package types

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPDFProcessingDefaultsAndLimits(t *testing.T) {
	p := DefaultPDFProcessingSettings()
	require.NoError(t, p.Validate())
	require.Equal(t, 1, p.OverlapPages)
	require.Equal(t, 24000, p.GroupingRequestBytes)
	require.Equal(t, 86400, p.QueueTimeoutSeconds)
	require.Equal(t, 7200, p.ExecutionTimeoutSeconds)
	for _, change := range []func(*PDFProcessingSettings){func(p *PDFProcessingSettings) { p.OverlapPages = 0 }, func(p *PDFProcessingSettings) { p.OverlapPages = 6 }, func(p *PDFProcessingSettings) { p.GroupingRequestBytes = 11999 }, func(p *PDFProcessingSettings) { p.GroupingRequestBytes = 96001 }, func(p *PDFProcessingSettings) { p.QueueTimeoutSeconds = 3599 }, func(p *PDFProcessingSettings) { p.QueueTimeoutSeconds = 86401 }, func(p *PDFProcessingSettings) { p.ExecutionTimeoutSeconds = 599 }, func(p *PDFProcessingSettings) { p.ExecutionTimeoutSeconds = 7201 }} {
		bad := p
		change(&bad)
		require.Error(t, bad.Validate())
	}
	cfg := &ParserEngineConfig{PDFProcessing: &p}
	incoming := &ParserEngineConfig{MinerUEndpoint: "https://parser.invalid"}
	merged := MergeParserEngineConfigForUpdate(incoming, cfg)
	require.Equal(t, p, *merged.PDFProcessing)
}
