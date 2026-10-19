import { fetchWithAuthRetry } from '../../utils/authFetch'

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

export function researchFramePath(workbenchId: string, view: string): string {
  const paths: Record<string, string> = { home: '/', diagram: '/sn/diagram', indicators: '/sn/indicators',
    judgment: '/sn/judgment', strategies: '/sn/strategies', reports: '/sn/reports' }
  if (!workbenchId || !Object.hasOwn(paths,view)) return ''
  return `/research/workbenches/${encodeURIComponent(workbenchId)}${paths[view]}`
}
