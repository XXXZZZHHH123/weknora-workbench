const PREFIX = 'weknora:session-model:v1'

// A browser draft belongs to one account, workspace and conversation. It is
// separate from the default used by the new-conversation composer.
export function sessionModelDraftKey(userId: string, tenantId: string | number | null, sessionId: string): string {
  if (!userId || !tenantId || !sessionId) return ''
  return `${PREFIX}:${[userId, String(tenantId), sessionId].map(encodeURIComponent).join(':')}`
}

export function readSessionModelDraft(key: string): string {
  if (!key) return ''
  try { return localStorage.getItem(key) || '' } catch { return '' }
}

export function writeSessionModelDraft(key: string, modelId: string): void {
  if (!key) return
  try {
    if (modelId) localStorage.setItem(key, modelId)
    else localStorage.removeItem(key)
  } catch {
    // A disabled/full browser store must not prevent sending the local choice.
  }
}
