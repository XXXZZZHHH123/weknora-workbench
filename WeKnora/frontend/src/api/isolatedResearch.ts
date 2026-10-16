export interface IsolatedResearchIdentity { token: string; tenantId: string; userId: string; workbenchId: string; knowledgeBaseId: string }
export interface WorkbenchSource {
  id: string; file_name: string; file_type?: string; parse_status?: string; folder_path?: string
  knowledge_base_id?: string; file_size?: number; source_kind?: string; [key: string]: unknown
}
export interface WorkbenchDataset { dataset_id?: string; id?: string; filename?: string; file_name?: string; name?: string; [key: string]: unknown }
export interface WorkbenchIndicator { series_id: string; name: string; unit?: string; frequency?: string; caliber?: Record<string, unknown>; [key: string]: unknown }
export interface WorkbenchOverview {
  source_inventory?: { knowledge_base_id: string; sources: WorkbenchSource[]; total?: number }
  workbench: { id: string; knowledge_base_id: string; workspace_mode: string }
  counts: { sources: number; documents_ready: number; excel_pending: number; indicators: number; observations: number }
  readiness: { status: 'empty' | 'preparing' | 'partially_ready' | 'ready'; tasks: Array<{ id: string; status: string; count?: number }> }
  modules: Array<{ id: string; status: string; reason?: string }>
}

export function createIsolatedResearchClient(captured: IsolatedResearchIdentity, current: () => IsolatedResearchIdentity, fetcher: typeof fetch = fetch) {
  const own = { ...captured }
  function assertIdentity() {
    const active = current()
    if (!own.token || !own.workbenchId || !own.knowledgeBaseId || (Object.keys(own) as Array<keyof IsolatedResearchIdentity>).some(key => active[key] !== own[key])) {
      throw new Error('账号、空间或工作台已切换，请重新操作。')
    }
  }
  async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
    assertIdentity()
    if (!/^\/(overview|sources|source-imports|scan-jobs|datasets|imports|indicators|structure|judgments|notes|briefs|strategies|exports)(\/|\?|$)/.test(path) || path.includes('..')) throw new Error('无效的工作台请求。')
    const headers = new Headers(options.headers)
    headers.set('Authorization', `Bearer ${own.token}`)
    headers.set('X-Tenant-ID', own.tenantId)
    headers.set('X-Research-User', own.userId)
    headers.set('X-Knowledge-Base-ID', own.knowledgeBaseId)
    const response = await fetcher(`/research/api/workbenches/${encodeURIComponent(own.workbenchId)}${path}`, { ...options, credentials: 'same-origin', headers })
    assertIdentity()
    const result = await response.json().catch(() => ({}))
    assertIdentity()
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : typeof result.message === 'string' ? result.message : '工作台处理失败，请重试。')
    return result as T
  }
  return {
    get: <T>(path: string) => request<T>(path),
    post: <T>(path: string, body: unknown) => request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
    put: <T>(path: string, body: unknown) => request<T>(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
    patch: <T>(path: string, body: unknown) => request<T>(path, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  }
}
