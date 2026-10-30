/**
 * Shared access-token refresh used by both axios and the chat SSE client.
 *
 * Refresh tokens rotate, so concurrent 401s must queue behind one in-flight
 * /auth/refresh. The SSE handshake runs on raw fetch and cannot use the
 * axios interceptor; both planes call {@link refreshAccessTokenShared}.
 */

import { publishAuthTokens } from './authSession'

const AUTH_STORAGE_KEYS = [
  'weknora_token',
  'weknora_refresh_token',
  'weknora_user',
  'weknora_tenant',
  'weknora_knowledge_bases',
  'weknora_current_kb',
  'weknora_selected_tenant_id',
  'weknora_selected_tenant_name',
  'weknora_memberships',
] as const

let isRefreshing = false
let failedQueue: Array<{
  resolve: (token: string) => void
  reject: (error: unknown) => void
}> = []

export type TokenRefreshResult = {
  success: boolean
  data?: { token: string; refreshToken: string }
  message?: string
}

export type RefreshAccessTokenOptions = {
  refresh?: (refreshToken: string) => Promise<TokenRefreshResult>
  rejectedToken?: string
  messages?: {
    pleaseRelogin: string
    tokenRefreshFailed: string
  }
}

const defaultMessages = {
  pleaseRelogin: 'Please log in again',
  tokenRefreshFailed: 'Token refresh failed',
}

export class StreamAuthError extends Error {
  constructor(readonly status: number) {
    super(`HTTP ${status}`)
    this.name = 'StreamAuthError'
  }
}

export function isStreamAuthError(err: unknown): boolean {
  if (!err || typeof err !== 'object') return false
  if (err instanceof StreamAuthError) return true
  return (err as { name?: string }).name === 'StreamAuthError'
}

export function isEmbedPage(): boolean {
  if (typeof window === 'undefined') return false
  return window.location.pathname.startsWith('/embed/')
}

export function redirectToLogin() {
  if (typeof window === 'undefined') return
  if (window.location.pathname === '/login') return
  if (isEmbedPage()) return
  window.location.href = '/login'
}

export function clearAuthStorage() {
  for (const key of AUTH_STORAGE_KEYS) {
    try {
      localStorage.removeItem(key)
    } catch {
      // Storage may be unavailable. Still attempt the remaining keys and
      // allow callers to reject waiting requests and redirect to login.
    }
  }
}

export function forceReloginRedirect() {
  clearAuthStorage()
  redirectToLogin()
}

function processQueue(error: unknown, token: string | null = null) {
  failedQueue.forEach(({ resolve, reject }) => {
    if (error) {
      reject(error)
    } else {
      resolve(token as string)
    }
  })
  failedQueue = []
}

async function defaultRefresh(refreshToken: string): Promise<TokenRefreshResult> {
  const { refreshToken: refreshTokenAPI } = await import('../api/auth/index')
  return refreshTokenAPI(refreshToken)
}

/**
 * Refresh the access token, de-duplicated across all callers.
 *
 * Resolves with the current access token. Invalid sessions redirect to login;
 * temporary failures preserve credentials so the operation can be retried.
 */
export async function refreshAccessTokenShared(
  options: RefreshAccessTokenOptions = {},
): Promise<string> {
  const messages = { ...defaultMessages, ...options.messages }
  const refresh = options.refresh ?? defaultRefresh
  if (isRefreshing) {
    return new Promise<string>((resolve, reject) => {
      failedQueue.push({ resolve, reject })
    })
  }

  isRefreshing = true
  let readingStorage = false
  let writingTokens = false
  let sessionCaptured = false
  let storedRefreshToken: string | null = null
  let sessionUser: string | null = null
  const stillOwnsSession = () => localStorage.getItem('weknora_refresh_token') === storedRefreshToken
    && localStorage.getItem('weknora_user') === sessionUser
  try {
    readingStorage = true
    const currentToken = localStorage.getItem('weknora_token')
    storedRefreshToken = localStorage.getItem('weknora_refresh_token')
    sessionUser = localStorage.getItem('weknora_user')
    sessionCaptured = true
    readingStorage = false
    // A delayed 401 may belong to a token already replaced by another caller.
    if (options.rejectedToken && currentToken && currentToken !== options.rejectedToken) {
      publishAuthTokens({ token: currentToken, refreshToken: storedRefreshToken || '' })
      processQueue(null, currentToken)
      return currentToken
    }
    if (!storedRefreshToken) {
      throw Object.assign(new Error(messages.pleaseRelogin), { invalidSession: true })
    }
    const response = await refresh(storedRefreshToken)
    readingStorage = true
    const ownsSession = stillOwnsSession()
    readingStorage = false
    if (!ownsSession) throw new Error('登录会话已改变，请重新操作')

    if (!response.success || !response.data?.token) {
      throw Object.assign(new Error(response.message || messages.tokenRefreshFailed), { invalidSession: true })
    }

    const { token, refreshToken: newRefreshToken } = response.data
    writingTokens = true
    localStorage.setItem('weknora_token', token)
    if (newRefreshToken) localStorage.setItem('weknora_refresh_token', newRefreshToken)
    writingTokens = false
    publishAuthTokens({ token, refreshToken: newRefreshToken || storedRefreshToken })
    processQueue(null, token)
    return token
  } catch (refreshError) {
    const failure = refreshError as { status?: number; invalidSession?: boolean }
    let mustRelogin = readingStorage || writingTokens
    if (!mustRelogin && sessionCaptured && (failure?.status === 401 || failure?.status === 403 || failure?.invalidSession)) {
      try {
        mustRelogin = stillOwnsSession()
      } catch {
        mustRelogin = true
      }
    }
    if (mustRelogin) forceReloginRedirect()
    processQueue(refreshError, null)
    throw refreshError
  } finally {
    isRefreshing = false
  }
}

/**
 * Replay an SSE handshake once after a 401. Refresh failure already
 * redirected; a second 401 after a successful refresh is surfaced to the
 * caller without wiping the newly minted session (axios does the same).
 *
 * Returns undefined when a newer send or an abort superseded the replay.
 */
export async function runStreamWithAuthRetry<T>(options: {
  run: (token: string) => Promise<T>
  initialToken: string
  isEmbed: boolean
  isCurrent: () => boolean
  refreshAccessToken: () => Promise<string>
  reloginMessage: string
}): Promise<T | undefined> {
  try {
    return await options.run(options.initialToken)
  } catch (err) {
    if (!isStreamAuthError(err) || options.isEmbed) throw err

    let refreshedToken: string
    try {
      refreshedToken = await options.refreshAccessToken()
    } catch {
      throw new Error(options.reloginMessage)
    }

    if (!options.isCurrent()) return undefined

    try {
      return await options.run(refreshedToken)
    } catch (retryErr) {
      if (isStreamAuthError(retryErr)) {
        throw new Error(options.reloginMessage)
      }
      throw retryErr
    }
  }
}

/** Reset module locks between unit tests. */
export function resetAuthRefreshStateForTests() {
  isRefreshing = false
  failedQueue = []
}
