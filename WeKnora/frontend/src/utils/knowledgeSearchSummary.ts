type Translate = (key: string, values?: Record<string, string | number>) => string

/** Source selection and content search share a tool name, but not a result type. */
export function knowledgeSearchSummary(t: Translate, data: any): string {
  if (!data) return ''
  if (data.display_type === 'document_info') {
    const count = Array.isArray(data.documents) ? data.documents.length : 0
    if (!count) return t('agentStream.search.noCandidateDocuments')
    const key = data.source_plan_validated === true ? 'confirmedDocuments'
      : data.requires_source_scope === true ? 'candidateDocuments' : 'foundDocuments'
    return t(`agentStream.search.${key}`, { count: `<strong>${count}</strong>` })
  }
  const hasCount = typeof data.count === 'number' && Number.isFinite(data.count) && data.count >= 0
  if (!Array.isArray(data.results) && !hasCount) return ''
  const count = Array.isArray(data.results) ? data.results.length : Math.floor(data.count)
  if (!count) return t('agentStream.search.noResults')
  const files = data.kb_counts ? Object.keys(data.kb_counts).length : 0
  return files
    ? t('agentStream.search.foundResultsFromFiles', { count: `<strong>${count}</strong>`, files: `<strong>${files}</strong>` })
    : t('agentStream.search.foundResults', { count: `<strong>${count}</strong>` })
}
