import { fetchWithAuthRetry } from '../utils/authFetch'

export type AnalysisJobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'interrupted' | 'expired' | 'applied'
export type AnalysisJobPurpose = 'judgments' | 'diagram'
export interface AnalysisJob {
  id: string
  workbench_id: string
  state: AnalysisJobState
  phase: string
  title: string
  created_at: string
  updated_at: string
  finished_at?: string
  expires_at?: string
  result_available: boolean
  purpose?: AnalysisJobPurpose
}
export interface AnalysisTaskOwner { userId: string; tenantId: string; token: string }
const states = new Set<AnalysisJobState>(['queued', 'running', 'succeeded', 'failed', 'interrupted', 'expired', 'applied'])
const phases = new Set(['queued', 'reading_source', 'loading_catalog', 'selecting', 'analyzing', 'generating', 'validating', ...states])

export function analysisJobId(value: unknown): string {
  return typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value) ? value : ''
}
export function readAnalysisJobs(value: unknown): AnalysisJob[] {
  if (!value || typeof value !== 'object' || !Array.isArray((value as { items?: unknown }).items)) throw new Error('暂时无法读取分析进度。')
  const jobs = new Map<string, AnalysisJob>()
  for (const row of (value as { items: unknown[] }).items.slice(0, 20)) {
    if (!row || typeof row !== 'object') continue
    const item = row as Record<string, unknown>, id = analysisJobId(item.id || item.job_id), workbench = analysisJobId(item.workbench_id)
    if (!id || !workbench || !states.has(item.state as AnalysisJobState)) continue
    const purpose = item.purpose === undefined ? 'judgments' : item.purpose
    if (purpose !== 'judgments' && purpose !== 'diagram') continue
    const timestamp = (name: string) => typeof item[name] === 'string' && String(item[name]).length <= 64 ? String(item[name]) : ''
    jobs.set(id, { id, workbench_id: workbench, state: item.state as AnalysisJobState, purpose,
      phase: typeof item.phase === 'string' && phases.has(item.phase) ? item.phase : '', title: purpose === 'diagram' ? '产业图配置生成' : '研究逻辑分析',
      created_at: timestamp('created_at'), updated_at: timestamp('updated_at'),
      ...(timestamp('finished_at') ? { finished_at: timestamp('finished_at') } : {}),
      ...(timestamp('expires_at') ? { expires_at: timestamp('expires_at') } : {}), result_available: item.result_available === true })
  }
  return [...jobs.values()]
}
export async function listAnalysisJobs(owner: AnalysisTaskOwner, isCurrent: () => boolean, fetcher?: typeof fetch): Promise<AnalysisJob[]> {
  const response = await fetchWithAuthRetry('/research/api/research/analysis-jobs?limit=20', {
    method: 'GET', credentials: 'same-origin', headers: { 'X-Tenant-ID': owner.tenantId, 'X-Research-User': owner.userId },
  }, { token: owner.token, isCurrent, ...(fetcher ? { fetcher } : {}) })
  if (!response.ok) {
    const error = new Error('暂时无法读取分析进度。') as Error & { status: number }
    error.status = response.status
    throw error
  }
  const result: unknown = await response.json()
  if (!isCurrent()) throw new Error('账号或空间已切换。')
  return readAnalysisJobs(result)
}
