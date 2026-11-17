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
  if (parseStatus === 'completed') parts.push('处理流程已结束')
  if (parseStatus === 'finalizing') parts.push('解析阶段已结束')

  const tableCount = result ? count(result.table_count) : undefined
  const acceptedCount = result ? count(result.accepted_count) : undefined
  const metadataRecord = metadata && typeof metadata === 'object'
    ? metadata as Record<string, unknown>
    : undefined
  const content = metadataRecord?._content_processing_v1
  const saved = content && typeof content === 'object' && !Array.isArray(content)
    ? content as Record<string, unknown> : undefined
  const textCount = count(saved?.text_chunk_count)
  parts.push(textCount === undefined ? '正文：未统计'
    : saved?.text_is_catalog === true ? `正文：未统计（另存目录 ${textCount} 分块）`
    : `正文：已保存 ${textCount} 个文本分块`)
  const imageCount = count(saved?.image_count)
  parts.push(imageCount === undefined ? '图片：未统计' : `图片：已保存 ${imageCount} 张`)
  const publishedCount = result ? count(result.published_table_count) : undefined
  if (publishedCount !== undefined) {
    parts.push(`表格：已入库 ${publishedCount} 个数据区块`)
  } else if (acceptedCount !== undefined && acceptedCount > 0) {
    parts.push(`表格：自动入库 ${acceptedCount} 张${tableCount !== undefined ? `（识别片段 ${tableCount}）` : ''}`)
  } else if (tableCount === 0 && acceptedCount === 0) {
    parts.push('表格：识别 0 张')
  } else if (tableCount !== undefined) {
    parts.push(`表格：识别片段 ${tableCount}，入库待核对`)
  } else {
    parts.push('表格：未统计')
  }
  const numericCount = result ? count(result.computable_value_count) : undefined
  parts.push(numericCount === undefined ? '可计算数据：未统计' : `可计算数据：已入库 ${numericCount} 个有效数值`)
  parts.push('内容覆盖尚未核验')

  if (result && Array.isArray(result.sheets)) {
    parts.push(`工作表结果 ${result.sheets.length} 项`)
  }
  const media = metadataRecord?.pptx_media_processing_v1
  if (media && typeof media === 'object' && !Array.isArray(media)) {
    const preview = media as Record<string, unknown>
    const deferred = count(preview.deferred_vector_previews)
    const unavailable = count(preview.unavailable_vector_parts)
    if (deferred !== undefined && deferred > 0 && deferred <= 4096) parts.push(`预览待生成 ${deferred}`)
    if (unavailable !== undefined && unavailable > 0 && unavailable <= 4096) parts.push(`原图不可用 ${unavailable}`)
  }
  if (receipt.attentionCount > 0) parts.push(`待处理 ${receipt.attentionCount}`)
  return parts
}
