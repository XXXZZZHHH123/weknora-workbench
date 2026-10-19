import { refreshAccessTokenShared } from './authRefresh'

/** Retry authentication failures once, while keeping the original resource scope. */
export async function fetchWithAuthRetry(input: string, options: RequestInit, context: {
  token: string
  isCurrent: () => boolean
  fetcher?: typeof fetch
  refresh?: (token: string) => Promise<string>
}): Promise<Response> {
  const fetcher = context.fetcher ?? fetch
  const check = () => {
    if (!context.isCurrent() || options.signal?.aborted) throw new Error('账号或空间已切换，请重新操作。')
  }
  const run = (token: string) => {
    check()
    const headers = new Headers(options.headers)
    headers.set('Authorization', `Bearer ${token}`)
    return fetcher(input, { ...options, headers })
  }
  let response = await run(context.token)
  check()
  if (response.status === 401) {
    const token = await (context.refresh ?? (rejectedToken => refreshAccessTokenShared({ rejectedToken })))(context.token)
    check()
    response = await run(token)
    check()
  }
  return response
}

/** Called only after the host has checked the iframe's origin and source. */
export async function refreshStructuredFrame(event: MessageEvent, isCurrent: () => boolean) {
  if (event.data?.type !== 'structured-refresh-auth' || typeof event.data.requestId !== 'string') return
  const user = localStorage.getItem('weknora_user')
  const tenant = localStorage.getItem('weknora_selected_tenant_id')
  const current = () => isCurrent() && user === localStorage.getItem('weknora_user') && tenant === localStorage.getItem('weknora_selected_tenant_id')
  try {
    const token = await refreshAccessTokenShared({ rejectedToken: event.data.token })
    if (current()) (event.source as Window).postMessage({ type: 'structured-auth-refreshed', requestId: event.data.requestId, token }, event.origin)
  } catch {
    if (current()) (event.source as Window).postMessage({ type: 'structured-auth-refreshed', requestId: event.data.requestId, error: '登录暂时无法刷新，请重试或重新登录。' }, event.origin)
  }
}
