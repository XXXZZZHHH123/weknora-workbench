package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var (
	requestedPagePattern = regexp.MustCompile(`第\s*([1-9][0-9]*)\s*页|(?i:page|slide)\s*([1-9][0-9]*)`)
	requestedReportDate  = regexp.MustCompile(`(?:(20[0-9]{2})\s*年\s*)?([0-9]{1,2})\s*月\s*([0-9]{1,2})\s*日`)
	reportTitleDate      = regexp.MustCompile(`(20[0-9]{2})[-_./年]?([01][0-9])[-_./月]?([0-3][0-9])`)
	reportSeparatedDate  = regexp.MustCompile(`(20[0-9]{2})[-_./]([0-9]{1,2})[-_./]([0-9]{1,2})`)
	requestedFilePattern = regexp.MustCompile(`[^\s"“”'‘’《》<>]+\.(?i:pptx?|pdf|docx?|xlsx?)`)
	comparativeReports   = regexp.MustCompile(`(?:其他|其它|另一|不同).{0,4}(?:报告|周报|日报|月报)|比较.{0,8}(?:报告|周报|日报|月报)`)
)

// RequestedDocumentConstraint binds a turn to the report the user named, not
// whichever same-name chart has the strongest embedding match. The constraint
// is shared by search and read tools and never broadens their authorized scope.
type RequestedDocumentConstraint struct {
	Page             int
	FileName         string
	ReportKind       string
	Year, Month, Day int
	mu               sync.Mutex
	resolved         bool
	document         *types.Knowledge
	err              error
}

func NewRequestedDocumentConstraint(query string) *RequestedDocumentConstraint {
	if comparativeReports.MatchString(query) {
		return nil
	}
	r := &RequestedDocumentConstraint{}
	if match := requestedPagePattern.FindStringSubmatch(query); len(match) > 0 {
		for _, value := range match[1:] {
			if value != "" {
				r.Page, _ = strconv.Atoi(value)
				break
			}
		}
	}
	if file := requestedFilePattern.FindString(query); file != "" {
		r.FileName = file
		return r
	}
	for _, kind := range []string{"周报", "日报", "月报", "年报"} {
		if strings.Contains(query, kind) {
			r.ReportKind = kind
			break
		}
	}
	match := requestedReportDate.FindStringSubmatch(query)
	if len(match) == 0 {
		match = reportSeparatedDate.FindStringSubmatch(query)
	}
	if len(match) == 0 {
		match = reportTitleDate.FindStringSubmatch(query)
	}
	if r.ReportKind == "" || len(match) == 0 {
		return nil
	}
	r.Year, _ = strconv.Atoi(match[1])
	r.Month, _ = strconv.Atoi(match[2])
	r.Day, _ = strconv.Atoi(match[3])
	if r.Month < 1 || r.Month > 12 || r.Day < 1 || r.Day > 31 {
		return nil
	}
	return r
}

func (r *RequestedDocumentConstraint) matches(k *types.Knowledge) bool {
	if documentIsRetired(k) {
		return false
	}
	if r.FileName != "" {
		return strings.EqualFold(strings.TrimSpace(k.FileName), r.FileName) || strings.EqualFold(strings.TrimSpace(k.Title), r.FileName)
	}
	title := k.Title + " " + k.FileName
	if !strings.Contains(title, r.ReportKind) {
		return false
	}
	dateText := title
	if metadata, err := k.Metadata.Map(); err == nil {
		for _, key := range []string{"report_date", "published_date", "date"} {
			if date, ok := metadata[key]; ok {
				dateText += " " + fmt.Sprint(date)
			}
		}
	}
	for _, date := range append(reportTitleDate.FindAllStringSubmatch(dateText, -1), reportSeparatedDate.FindAllStringSubmatch(dateText, -1)...) {
		year, _ := strconv.Atoi(date[1])
		month, _ := strconv.Atoi(date[2])
		day, _ := strconv.Atoi(date[3])
		if (r.Year == 0 || year == r.Year) && month == r.Month && day == r.Day {
			return true
		}
	}
	for _, date := range requestedReportDate.FindAllStringSubmatch(dateText, -1) {
		year, _ := strconv.Atoi(date[1])
		month, _ := strconv.Atoi(date[2])
		day, _ := strconv.Atoi(date[3])
		if (r.Year == 0 || year == r.Year) && month == r.Month && day == r.Day {
			return true
		}
	}
	return false
}

// Deletion tombstones can remain visible while asynchronous cleanup removes
// chunks. They are neither ambiguous candidates nor usable explicit sources.
func documentIsRetired(k *types.Knowledge) bool {
	if k == nil || k.DeletedAt.Valid {
		return true
	}
	for _, status := range []string{k.ParseStatus, k.EnableStatus} {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case types.ParseStatusDeleting, "deleted", "archived":
			return true
		}
	}
	return false
}

func (r *RequestedDocumentConstraint) resolve(ctx context.Context, service interfaces.KnowledgeService, targets types.SearchTargets) (*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved {
		return r.document, r.err
	}
	if service == nil {
		return nil, fmt.Errorf("无法定位指定报告：知识库服务不可用；不能用其他报告替代")
	}
	lister := NewListDocumentsTool(service, targets)
	keyword := r.ReportKind
	if r.FileName != "" {
		keyword = r.FileName
	}
	seen := map[string]bool{}
	var candidates []*types.Knowledge
	for _, kbID := range targets.GetAllKnowledgeBaseIDs() {
		for page := 1; ; page++ {
			rows, total, err := lister.listScoped(ctx, kbID, keyword, page, listDocumentsMaxPageSize)
			if err != nil {
				return nil, fmt.Errorf("无法定位指定报告：%w；不能用其他报告替代", err)
			}
			for _, k := range rows {
				if !seen[k.ID] && r.matches(k) {
					seen[k.ID] = true
					candidates = append(candidates, k)
				}
			}
			if int64(page*listDocumentsMaxPageSize) >= total {
				break
			}
			if page*listDocumentsMaxPageSize >= listDocumentsScopeCap {
				return nil, fmt.Errorf("指定报告的候选文件过多，请提供完整文件名；不能用其他报告替代")
			}
		}
	}
	r.resolved = true
	if len(candidates) != 1 {
		names := make([]string, 0, len(candidates))
		for _, k := range candidates {
			names = append(names, k.Title)
		}
		if len(candidates) == 0 {
			r.err = fmt.Errorf("当前可访问知识库未找到指定报告，请确认文件名、日期或上传状态；不能用其他报告的同名图替代")
		} else {
			r.err = fmt.Errorf("指定报告存在 %d 个候选（%s），请明确年份或完整文件名；不能随意选择或混用其他报告", len(candidates), strings.Join(names, "；"))
		}
		return nil, r.err
	}
	r.document = candidates[0]
	return r.document, nil
}

func restrictDocumentTargets(ctx context.Context, targets types.SearchTargets, service interfaces.KnowledgeService, ids []string) (types.SearchTargets, error) {
	var result types.SearchTargets
	for _, id := range dedupNonEmptyStrings(ids) {
		k, err := authorizeKnowledgeInSearchTargets(ctx, targets, id, service)
		if err != nil {
			return nil, err
		}
		if documentIsRetired(k) {
			return nil, fmt.Errorf("指定文档已删除、正在删除或已归档，不能作为查询来源")
		}
		result = append(result, &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: k.KnowledgeBaseID, TenantID: targets.GetTenantIDForKB(k.KnowledgeBaseID), KnowledgeIDs: []string{k.ID}, DisableRecallThresholds: true})
	}
	return result, nil
}
