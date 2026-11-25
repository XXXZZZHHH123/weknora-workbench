/** Server-owned source-processing receipt, independent of document or question text. */
export interface KnowledgeIngestion {
  status?: string
  stage?: string
  attentionCount: number
  result?: Record<string, unknown>
}

const count = (value: unknown): number | undefined =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined

export function knowledgeIngestion(metadata: unknown): KnowledgeIngestion {
  if (!metadata || typeof metadata !== 'object') return { attentionCount: 0 }
  const value = (metadata as Record<string, unknown>)._knowledge_ingestion
  if (!value || typeof value !== 'object') return { attentionCount: 0 }
  const receipt = value as Record<string, unknown>
  const result = receipt.result && typeof receipt.result === 'object' && !Array.isArray(receipt.result)
    ? receipt.result as Record<string, unknown>
    : undefined
  const attention = count(receipt.attention_count) ?? count(result?.attention_count) ?? 0
  return {
    status: typeof receipt.status === 'string' ? receipt.status : undefined,
    stage: typeof receipt.stage === 'string' ? receipt.stage.slice(0, 500) : undefined,
    attentionCount: attention <= 5000 ? attention : 0,
    result,
  }
}

/**
 * Human-readable content receipt for file cards and rows. Only explicit counts
 * persisted by parsing/structured ingestion are shown; absent evidence stays
 * absent, and auto-published tables are never described as human reviewed.
 */
export function knowledgeCoverageSummary(metadata: unknown, parseStatus?: string): string[] {
  const receipt = knowledgeIngestion(metadata)
  const result = receipt.result
  const parts: string[] = []
  if (parseStatus === 'finalizing') parts.push('正文已可搜索，其他内容仍在整理')

  const tableCount = result ? count(result.table_count) : undefined
  const acceptedCount = result ? count(result.accepted_count) : undefined
  const metadataRecord = metadata && typeof metadata === 'object'
    ? metadata as Record<string, unknown>
    : undefined
  const content = metadataRecord?._content_processing_v1
  const saved = content && typeof content === 'object' && !Array.isArray(content)
    ? content as Record<string, unknown> : undefined
  const textCount = count(saved?.text_chunk_count)
  parts.push(textCount === undefined ? '正文数量未统计'
    : saved?.text_is_catalog === true ? `已保存目录中的 ${textCount} 段，正文收录范围尚未确认`
    : `已保存 ${textCount} 段正文`)
  const imageCount = count(saved?.image_count)
  parts.push(imageCount === undefined ? '图片：未统计' : `图片：已保存 ${imageCount} 张`)
  const publishedCount = result ? count(result.published_table_count) : undefined
  if (publishedCount !== undefined) {
    parts.push(publishedCount ? `已有 ${publishedCount} 个表格区域可查询` : '暂时没有可查询的表格区域')
  } else if (acceptedCount !== undefined && acceptedCount > 0) {
    parts.push(`已自动整理 ${acceptedCount} 张表，尚待人工核对${tableCount !== undefined ? `（识别到 ${tableCount} 处表格内容）` : ''}`)
  } else if (tableCount === 0 && acceptedCount === 0) {
    parts.push('没有识别到表格')
  } else if (tableCount !== undefined) {
    parts.push(`识别到 ${tableCount} 处表格内容，请核对哪些可以使用`)
  } else {
    parts.push('表格数量未统计')
  }
  const numericCount = result ? count(result.computable_value_count) : undefined
  parts.push(numericCount === undefined ? '可计算的数值尚未统计' : `已有 ${numericCount} 个数值可用于计算`)
  parts.push('当前结果可能只包含原件中的部分内容')

  if (result && Array.isArray(result.sheets)) {
    parts.push(`可查看 ${result.sheets.length} 张工作表的处理结果`)
  }
  const media = metadataRecord?.pptx_media_processing_v1
  if (media && typeof media === 'object' && !Array.isArray(media)) {
    const preview = media as Record<string, unknown>
    const deferred = count(preview.deferred_vector_previews)
    const unavailable = count(preview.unavailable_vector_parts)
    if (deferred !== undefined && deferred > 0 && deferred <= 4096) parts.push(`预览待生成 ${deferred}`)
    if (unavailable !== undefined && unavailable > 0 && unavailable <= 4096) parts.push(`原图不可用 ${unavailable}`)
  }
  if (receipt.attentionCount > 0) parts.push(`${receipt.attentionCount} 处需要核对，请打开文件查看处理结果`)
  return parts
}
