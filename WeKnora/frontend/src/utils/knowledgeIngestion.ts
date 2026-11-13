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
  if (acceptedCount !== undefined && acceptedCount > 0) {
    parts.push(`自动入库 ${acceptedCount} 张表`)
  } else if (tableCount === 0 && acceptedCount === 0) {
    parts.push('未识别到结构化表格')
  } else if (tableCount !== undefined) {
    parts.push(`识别到 ${tableCount} 张表`)
  }

  if (result && Array.isArray(result.sheets)) {
    parts.push(`工作表结果 ${result.sheets.length} 项`)
  }
  const metadataRecord = metadata && typeof metadata === 'object'
    ? metadata as Record<string, unknown>
    : undefined
  const imageCount = count(metadataRecord?.image_count) ?? count(metadataRecord?.images_found)
  if (imageCount !== undefined) parts.push(`图片 ${imageCount}`)
  const media = metadataRecord?.pptx_media_processing_v1
  if (media && typeof media === 'object' && !Array.isArray(media)) {
    const preview = media as Record<string, unknown>
    const deferred = count(preview.deferred_vector_previews)
    const unavailable = count(preview.unavailable_vector_parts)
    if (deferred !== undefined && deferred > 0 && deferred <= 4096) parts.push(`预览待生成 ${deferred}`)
    if (unavailable !== undefined && unavailable > 0 && unavailable <= 4096) parts.push(`原图不可用 ${unavailable}`)
  }
  const chunkCount = count(metadataRecord?.chunk_count)
  if (chunkCount !== undefined) parts.push(`分块 ${chunkCount}`)
  if (receipt.attentionCount > 0) parts.push(`待处理 ${receipt.attentionCount}`)
  return parts
}
