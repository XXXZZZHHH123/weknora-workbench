import { fetchWithAuthRetry } from '../../utils/authFetch'

export type MaterialScope = { token: string; userId: string; tenantId: string }
export type LibraryOperation = {
  id: string; asset_id: string; purpose: string; status: string; error?: string; result?: any;
  remote_ids?: Record<string, string>; created_at?: string; updated_at?: string;
  request?: Record<string, any>; parse_status?: string;
  status_checked?: boolean; status_refresh_error?: string;
}
export type LibraryAsset = {
  id: string; sha256: string; filename: string; size: number; kind: string; created_at: string;
  capabilities: { knowledge: boolean; structured: boolean; research: boolean; structured_reason?: string };
  shared_research: boolean; shared_structured?: boolean; operations?: LibraryOperation[];
}

export function createMaterialClient(scope: MaterialScope, current: () => MaterialScope, fetcher: typeof fetch = fetch) {
  const assertScope = () => {
    const active = current()
    if (active.userId !== scope.userId || active.tenantId !== scope.tenantId || !active.token) throw new Error('账号或空间已切换，请重新操作。')
  }
  async function request(path: string, options: RequestInit = {}, blob = false) {
    assertScope()
    if (!path.startsWith('/api/library') && !path.startsWith('/api/sn/indicators')) throw new Error('无效资料请求')
    const headers = new Headers(options.headers)
    headers.set('Authorization', `Bearer ${scope.token}`)
    headers.set('X-Tenant-ID', scope.tenantId)
    headers.set('X-Research-User', scope.userId)
    const response = await fetchWithAuthRetry('/research' + path, { ...options, credentials: 'same-origin', headers }, { token: current().token, fetcher, isCurrent: () => { try { assertScope(); return true } catch { return false } } })
    assertScope()
    if (blob && response.ok) {
      const result = await response.blob()
      assertScope()
      return result
    }
    const result = await response.json().catch(() => ({}))
    assertScope()
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '资料处理失败，请稍后重试。')
    return result
  }
  return {
    get: (path: string) => request(path),
    post: (path: string, body: unknown) => request(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
    upload: (file: File) => { const form = new FormData(); form.append('file', file); return request('/api/library/upload', { method: 'POST', body: form }) },
    download: (id: string) => request('/api/library/' + encodeURIComponent(id) + '/download', {}, true),
  }
}
