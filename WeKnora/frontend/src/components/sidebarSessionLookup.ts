import type { SessionForGrouping } from './sessionGrouping'

interface SessionResponse { success?: boolean; data?: SessionForGrouping }

export async function lookupSidebarSession(
  sessionId: string,
  cached: readonly SessionForGrouping[],
  fetchSession: (id: string) => Promise<SessionResponse>,
): Promise<SessionForGrouping | null> {
  if (!sessionId) return null
  const local = cached.find((row) => row.id === sessionId)
  if (local) return local
  const response = await fetchSession(sessionId)
  return response.success !== false && response.data?.id === sessionId && response.data.has_user_message === true ? response.data : null
}
