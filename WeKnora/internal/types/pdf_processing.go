package types

import "fmt"

// PDFProcessingSettings is a workspace-admin policy, never document input.
// Queue residence and actual parser execution use independent budgets.
type PDFProcessingSettings struct {
	OverlapPages            int `json:"overlap_pages"`
	GroupingRequestBytes    int `json:"grouping_request_bytes"`
	QueueTimeoutSeconds     int `json:"queue_timeout_seconds"`
	ExecutionTimeoutSeconds int `json:"execution_timeout_seconds"`
}

func DefaultPDFProcessingSettings() PDFProcessingSettings {
	return PDFProcessingSettings{OverlapPages: 1, GroupingRequestBytes: 24000,
		QueueTimeoutSeconds: 86400, ExecutionTimeoutSeconds: 7200}
}

func EffectivePDFProcessingSettings(cfg *ParserEngineConfig) PDFProcessingSettings {
	if cfg != nil && cfg.PDFProcessing != nil {
		return *cfg.PDFProcessing
	}
	return DefaultPDFProcessingSettings()
}

func (p PDFProcessingSettings) Validate() error {
	if p.OverlapPages < 1 || p.OverlapPages > 5 {
		return fmt.Errorf("重叠页数须为1至5页")
	}
	if p.GroupingRequestBytes < 12000 || p.GroupingRequestBytes > 96000 {
		return fmt.Errorf("每批内容上限须为12至96KB")
	}
	if p.QueueTimeoutSeconds < 3600 || p.QueueTimeoutSeconds > 86400 {
		return fmt.Errorf("排队保留时间须为1至24小时")
	}
	if p.ExecutionTimeoutSeconds < 600 || p.ExecutionTimeoutSeconds > 7200 {
		return fmt.Errorf("实际解析时限须为10至120分钟")
	}
	return nil
}

// RemotePDFJobReference lives only in the durable task payload, not public
// knowledge metadata. Keeping one lease across polls makes cancellation safe.
type RemotePDFJobReference struct {
	ID       string `json:"id"`
	QueuedAt int64  `json:"queued_at,omitempty"`
	LeaseID  string `json:"lease_id"`
	Poll     int    `json:"poll"`
}
