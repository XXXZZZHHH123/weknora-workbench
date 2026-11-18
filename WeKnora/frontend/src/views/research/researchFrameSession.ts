import { fetchWithAuthRetry } from '../../utils/authFetch'
import { analysisJobId, isAnalysisJobView, type AnalysisJobView } from '../../api/researchAnalysisTasks'
export { analysisJobId, isAnalysisJobView } from '../../api/researchAnalysisTasks'

// Cookie exchanges are serialized across mounts. A slow request for account A
// must finish before B's Set-Cookie, even when A's component has disappeared.
let exchangeTail: Promise<unknown> = Promise.resolve()

export function authenticateResearchFrame(token: string, tenant: number | string, isCurrent: () => boolean): Promise<boolean> {
  const exchange = exchangeTail.catch(() => undefined).then(async () => {
    if (!isCurrent()) return false
    const response = await fetchWithAuthRetry('/research/auth/session', {
      method: 'POST', credentials: 'same-origin',
      headers: { Authorization: `Bearer ${token}`, 'X-Tenant-ID': String(tenant) },
    }, { token, isCurrent })
    if (!isCurrent()) return false
    if (!response.ok) throw new Error('研究工作台登录核验失败，请重新登录后重试。')
    return true
  }).catch(error => { if (!isCurrent()) return false; throw error })
  exchangeTail = exchange.catch(() => undefined)
  return exchange
}

export function analysisJudgmentId(value: unknown): string {
  const text = typeof value === 'number' && Number.isSafeInteger(value) ? String(value) : typeof value === 'string' ? value : ''
  return /^[1-9][0-9]{0,18}$/.test(text) && BigInt(text) <= 9223372036854775807n ? text : ''
}

const paths = {
  home: '/', diagram: '/sn/diagram', indicators: '/sn/indicators', judgment: '/sn/judgment',
  strategies: '/sn/strategies', reports: '/sn/reports', monitors: '/', 'chart-import': '/sn/charts/import',
  dimensions: '/sn/dimensions', market: '/sn',
} as const satisfies Record<AnalysisJobView | 'indicators', string>

export function isResearchView(value: unknown): value is keyof typeof paths {
  return typeof value === 'string' && Object.hasOwn(paths, value)
}

export function researchFramePath(workbenchId: string, view: string, analysisJob?: unknown, judgment?: unknown, diagram?: unknown): string {
  if (!workbenchId || !isResearchView(view)) return ''
  const query = new URLSearchParams()
  const jobId = analysisJobId(analysisJob)
  if (isAnalysisJobView(view) && jobId) query.set('analysis_job', jobId)
  if (view === 'judgment' && analysisJudgmentId(judgment)) query.set('judgment_id', analysisJudgmentId(judgment))
  if (view === 'diagram' && analysisJudgmentId(diagram)) query.set('template', analysisJudgmentId(diagram))
  const hash = view === 'chart-import' ? '#report-chart-import' : ''
  return `/research/workbenches/${encodeURIComponent(workbenchId)}${paths[view]}${query.size ? '?' + query : ''}${hash}`
}
