/** Server-owned source-processing receipt, independent of document or question text. */
export interface KnowledgeIngestion {
  status?: string
  stage?: string
  attentionCount: number
}
export function knowledgeIngestion(metadata: unknown): KnowledgeIngestion {
  if (!metadata || typeof metadata !== 'object') return { attentionCount: 0 }
  const value = (metadata as Record<string, unknown>)._knowledge_ingestion
  if (!value || typeof value !== 'object') return { attentionCount: 0 }
  const receipt = value as Record<string, unknown>
  const count = receipt.attention_count
  return {
    status: typeof receipt.status === 'string' ? receipt.status : undefined,
    stage: typeof receipt.stage === 'string' ? receipt.stage.slice(0, 500) : undefined,
    attentionCount: typeof count === 'number' && Number.isInteger(count) && count > 0 && count <= 5000 ? count : 0,
  }
}
