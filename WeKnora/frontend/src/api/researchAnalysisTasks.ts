import { fetchWithAuthRetry } from '../utils/authFetch'

export type AnalysisJobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'interrupted' | 'expired' | 'applied' | 'superseded'
export const analysisJobPresentation = {
  judgments: { title: '研究逻辑分析', view: 'judgment', completed: '研究分析已完成' },
  diagram: { title: '产业图配置生成', view: 'diagram', completed: '产业图配置已生成' },
  monitor_draft: { title: '监测草稿生成', view: 'monitors' },
  report_dimension_draft: { title: '日报周报结论起草', view: 'reports' },
  report_chart_import: { title: '往期周报图表识别', view: 'chart-import' },
  dimension_inference: { title: '周报研究维度归纳', view: 'dimensions' },
  dimension_fields: { title: '研究维度指标建议', view: 'dimensions' },
  focus_suggestions: { title: '核心关注建议生成', view: 'market' },
  judgment_dimensions: { title: '研究判断维度补标', view: 'judgment' },
  strategy_text: { title: '策略原文整理', view: 'strategies' },
  strategy_generation: { title: '策略方案生成', view: 'strategies' },
} as const satisfies Record<string, { title: string; view: string; completed?: string }>
export type AnalysisJobPurpose = keyof typeof analysisJobPresentation
export type AnalysisJobView = (typeof analysisJobPresentation)[AnalysisJobPurpose]['view']
const analysisViews = new Set<unknown>(Object.values(analysisJobPresentation).map(presentation => presentation.view))

export function isAnalysisJobView(value: unknown): value is AnalysisJobView {
  return analysisViews.has(value)
}

function isAnalysisJobPurpose(value: unknown): value is AnalysisJobPurpose {
  return typeof value === 'string' && Object.hasOwn(analysisJobPresentation, value)
}
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
  dismissed?: boolean
  dismissed_at?: string
}
export interface AnalysisTaskOwner { userId: string; tenantId: string; token: string }
const states = new Set<AnalysisJobState>(['queued', 'running', 'succeeded', 'failed', 'interrupted', 'expired', 'applied', 'superseded'])
const phases = new Set(['reading_source', 'loading_catalog', 'selecting', 'analyzing', 'generating', 'validating',
  'dimension_queries', 'dimension_fields', ...states])

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
    if (!isAnalysisJobPurpose(purpose)) continue
    const timestamp = (name: string) => typeof item[name] === 'string' && String(item[name]).length <= 64 ? String(item[name]) : ''
    jobs.set(id, { id, workbench_id: workbench, state: item.state as AnalysisJobState, purpose,
      phase: typeof item.phase === 'string' && phases.has(item.phase) ? item.phase : '', title: analysisJobPresentation[purpose].title,
      created_at: timestamp('created_at'), updated_at: timestamp('updated_at'),
      ...(timestamp('finished_at') ? { finished_at: timestamp('finished_at') } : {}),
      ...(timestamp('expires_at') ? { expires_at: timestamp('expires_at') } : {}),
      ...(timestamp('dismissed_at') ? { dismissed_at: timestamp('dismissed_at') } : {}),
      dismissed: item.dismissed === true && item.state !== 'queued' && item.state !== 'running',
      result_available: item.result_available === true })
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

export async function dismissAnalysisJob(owner: AnalysisTaskOwner, id: string, isCurrent: () => boolean, fetcher?: typeof fetch): Promise<AnalysisJob> {
  if (!analysisJobId(id) || !isCurrent()) throw new Error('任务或当前账号已改变，请刷新后重试。')
  const response = await fetchWithAuthRetry(`/research/api/research/analysis-jobs/${encodeURIComponent(id)}/dismiss`, {
    method: 'POST', credentials: 'same-origin', headers: { 'X-Tenant-ID': owner.tenantId, 'X-Research-User': owner.userId },
  }, { token: owner.token, isCurrent, ...(fetcher ? { fetcher } : {}) })
  if (!response.ok) {
    const error = new Error(response.status === 409 ? '正在运行的任务不能忽略，请刷新进度。' : '暂时无法忽略此提示，请稍后重试。') as Error & { status: number }
    error.status = response.status
    throw error
  }
  const result: unknown = await response.json()
  if (!isCurrent()) throw new Error('账号或空间已切换。')
  const job = readAnalysisJobs({ items: [result] })[0]
  if (!job || job.id !== id || job.dismissed !== true) throw new Error('忽略状态未能核对，请刷新后重试。')
  return job
}

export function readDismissedAnalysisJobIds(value: unknown): string[] {
  const ids = value && typeof value === 'object' ? (value as { dismissed_ids?: unknown }).dismissed_ids : undefined
  if (!Array.isArray(ids) || ids.length > 512 || ids.some(id => !analysisJobId(id)) || new Set(ids).size !== ids.length)
    throw new Error('忽略状态未能核对，请刷新后重试。')
  return [...ids] as string[]
}

export async function dismissAllAnalysisJobs(owner: AnalysisTaskOwner, isCurrent: () => boolean, fetcher?: typeof fetch): Promise<string[]> {
  if (!isCurrent()) throw new Error('账号或空间已切换。')
  const response = await fetchWithAuthRetry('/research/api/research/analysis-jobs/dismiss-all', {
    method: 'POST', credentials: 'same-origin', headers: { 'X-Tenant-ID': owner.tenantId, 'X-Research-User': owner.userId },
  }, { token: owner.token, isCurrent, ...(fetcher ? { fetcher } : {}) })
  if (!response.ok) {
    const error = new Error('暂时无法忽略全部提示，请稍后重试。') as Error & { status: number }
    error.status = response.status
    throw error
  }
  const result: unknown = await response.json()
  if (!isCurrent()) throw new Error('账号或空间已切换。')
  return readDismissedAnalysisJobIds(result)
}
