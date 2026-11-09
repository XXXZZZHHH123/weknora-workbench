package tools

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const readDocumentMaxPages = 8

func validateDocumentPages(pages []int, maxPages int) error {
	if len(pages) == 0 || len(pages) > maxPages {
		return fmt.Errorf("physical pages must contain 1 to %d pages", maxPages)
	}
	seen := map[int]bool{}
	for _, page := range pages {
		if page < 1 || seen[page] {
			return fmt.Errorf("physical pages must be distinct positive integers")
		}
		seen[page] = true
	}
	return nil
}

func documentSelectionHints(selection DocumentSourceSelection) []int {
	if selection.Page > 0 {
		return []int{selection.Page}
	}
	return selection.Pages
}

func validateDocumentPageHints(selection DocumentSourceSelection) error {
	if selection.Page < 0 || selection.Page > 0 && len(selection.Pages) > 0 {
		return fmt.Errorf("use either page or pages with positive physical page numbers")
	}
	if len(selection.Pages) > 0 {
		return validateDocumentPages(selection.Pages, readDocumentMaxPages)
	}
	return nil
}

func (r *RequestedDocumentConstraint) validateUserPageConstraint(constraint *DocumentPageConstraint) error {
	if err := validateDocumentPages(constraint.Pages, 64); err != nil {
		return err
	}
	if strings.TrimSpace(constraint.UserQuote) == "" || utf8.RuneCountInString(constraint.UserQuote) > 2000 {
		return fmt.Errorf("用户页码限制需要真实用户原文依据")
	}
	for _, message := range r.userMessages {
		if strings.Contains(message, constraint.UserQuote) {
			return nil
		}
	}
	return fmt.Errorf("用户页码限制的引用不在实际用户消息中；模型临时选页应使用 page/pages 读取计划")
}

func sameDocumentPages(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for _, page := range left {
		if !slices.Contains(right, page) {
			return false
		}
	}
	return true
}

func cloneDocumentPageConstraint(constraint *DocumentPageConstraint) *DocumentPageConstraint {
	if constraint == nil {
		return nil
	}
	return &DocumentPageConstraint{Pages: append([]int(nil), constraint.Pages...), UserQuote: constraint.UserQuote}
}

func cloneDocumentSelection(selection DocumentSourceSelection) DocumentSourceSelection {
	selection.Pages = append([]int(nil), selection.Pages...)
	selection.UserPages = cloneDocumentPageConstraint(selection.UserPages)
	if selection.Date != nil {
		date := *selection.Date
		selection.Date = &date
	}
	return selection
}

// resolveReadPages keeps source binding separate from this call's navigation.
// A chunk handle or query must never be silently replaced by a model read hint.
func resolveReadPages(input ReadDocumentInput, selection DocumentSourceSelection, isChunk bool) ([]int, error) {
	if input.Page < 0 || input.Page > 0 && len(input.Pages) > 0 {
		return nil, fmt.Errorf("use either page or pages with positive physical page numbers")
	}
	pages := append([]int(nil), input.Pages...)
	if input.Page > 0 {
		pages = []int{input.Page}
	}
	if len(pages) > 0 {
		if err := validateDocumentPages(pages, readDocumentMaxPages); err != nil {
			return nil, err
		}
	}
	if selection.UserPages != nil {
		if len(pages) == 0 {
			if len(selection.UserPages.Pages) > readDocumentMaxPages {
				return nil, fmt.Errorf("用户限定多个物理页；每次 pages 读取最多 %d 页，请在限定范围内分批读取", readDocumentMaxPages)
			}
			pages = append([]int(nil), selection.UserPages.Pages...)
		}
		for _, page := range pages {
			if !slices.Contains(selection.UserPages.Pages, page) {
				return nil, fmt.Errorf("请求物理页不在用户明确限定的范围内；模型读取计划不能改变该限制")
			}
		}
	} else if len(pages) == 0 && input.SourceScope != nil && !isChunk && strings.TrimSpace(input.Query) == "" {
		pages = append([]int(nil), documentSelectionHints(selection)...)
	}
	return pages, nil
}
