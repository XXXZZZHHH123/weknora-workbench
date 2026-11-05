import { get, getDown } from '@/utils/request'
import type { SourceLocator } from '@/utils/sourceLocator'

export type EvidenceReference = {
  id: string
  kind: 'raw_cell' | 'structured_result' | 'document'
  result_id: string
  result_hash: string
  candidate_hash?: string
  title?: string
  summary?: string
  artifact_ids?: string[]
  version_status?: string
  review_status?: 'unverified'
}

export type EvidenceUnit = {
  status?: string
  symbol?: string
  evidence_level?: string
  binding_scope?: string
  expression?: string
  quantity?: string
  reason?: string
}

export type EvidenceColumnMetadata = {
  index?: number
  name?: string
  display_name?: string
  unit_metadata?: EvidenceUnit
}

export type EvidenceDetail = {
  reference: EvidenceReference
  status: 'snapshot' | 'stale' | 'unavailable'
  message?: string
  sources?: Array<{
    knowledge_base_id: string
    knowledge_id: string
    title: string
    file_name: string
    source_sha256: string
    locators?: SourceLocator[]
  }>
  locators?: SourceLocator[]
  manifest?: {
    result?: { columns?: Array<string | { name?: string; display_name?: string }>; rows?: unknown[][] }
    selector?: Record<string, unknown>
    unit?: EvidenceUnit
    column_metadata?: EvidenceColumnMetadata[]
    source_cells?: Array<Record<string, unknown>>
    computation?: {
      sql?: string
      expression?: string
      execution?: Record<string, unknown>
      input_scope?: unknown
      input_count?: number
      input_complete?: boolean
      coverage?: unknown
      source_truncated?: boolean
      sql_result_lineage_verified?: boolean
      mappings?: Array<{ source_rows?: number; source_columns?: number; projection_output_rows?: number; mapping: Record<string, unknown> }>
    }
    quality?: Record<string, unknown>
  }
}

const base = (sessionId: string, messageId: string, evidenceId: string) =>
  `/api/v1/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(messageId)}/evidence/${encodeURIComponent(evidenceId)}`

export async function getMessageEvidence(sessionId: string, messageId: string, evidenceId: string): Promise<EvidenceDetail> {
  const response = await get<{ success: boolean; data: EvidenceDetail }>(base(sessionId, messageId, evidenceId))
  return response.data
}

/** Fetch verified bytes through the evidence route, never an unversioned original. */
export async function previewEvidenceSource(sessionId: string, messageId: string, evidenceId: string, sourceIndex: number): Promise<Blob> {
  return getDown(`${base(sessionId, messageId, evidenceId)}/sources/${sourceIndex}/preview`)
}
