import { fetchWithAuthRetry } from '../../utils/authFetch'
import { analysisJobId } from '../../api/researchAnalysisTasks'
export { analysisJobId } from '../../api/researchAnalysisTasks'

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

export function researchFramePath(workbenchId: string, view: string, analysisJob?: unknown, judgment?: unknown): string {
  const paths: Record<string, string> = { home: '/', diagram: '/sn/diagram', indicators: '/sn/indicators',
    judgment: '/sn/judgment', strategies: '/sn/strategies', reports: '/sn/reports' }
  if (!workbenchId || !Object.hasOwn(paths,view)) return ''
  const query = new URLSearchParams()
  if (view === 'judgment') {
    if (analysisJobId(analysisJob)) query.set('analysis_job', analysisJobId(analysisJob))
    if (analysisJudgmentId(judgment)) query.set('judgment_id', analysisJudgmentId(judgment))
  }
  return `/research/workbenches/${encodeURIComponent(workbenchId)}${paths[view]}${query.size ? '?' + query : ''}`
}
