/** Serialize original-sheet reads across all source cards in this client. */
export function createStructuredSourceReadQueue() {
  let tail: Promise<void> = Promise.resolve()
  return function enqueue<T>(isCurrent: () => boolean, read: () => Promise<T>): Promise<T | undefined> {
    const result = tail.then(() => isCurrent() ? read() : undefined)
    // A failed older read must release the queue for the next current target.
    tail = result.then(() => undefined, () => undefined)
    return result.then(value => isCurrent() ? value : undefined)
  }
}

export const queueStructuredSourceRead = createStructuredSourceReadQueue()

function sourceConflictDetail(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 280 || /[<>\u0000-\u001f\u007f]/.test(value)) return null
  return value.trim() || null
}

async function boundedConflictJSON(response: Response): Promise<unknown> {
  if (!/^application\/(?:[a-z0-9.+-]+\+)?json\b/i.test(response.headers.get('content-type') || '') || !response.body) return null
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let bytes = 0, text = ''
  try {
    for (;;) {
      const next = await reader.read()
      if (next.done) break
      bytes += next.value.byteLength
      if (bytes > 4096) { await reader.cancel(); return null }
      text += decoder.decode(next.value, { stream: true })
    }
    return JSON.parse(text + decoder.decode())
  } catch { return null } finally { reader.releaseLock() }
}

/** Display only bounded, plain validation details from a JSON conflict. */
export async function structuredSourceReadError(response: Response): Promise<Error> {
  if (response.status === 401 || response.status === 403) return new Error('当前账号或知识库无权读取原始来源。')
  if (response.status === 404) return new Error('原始来源或对应数据集已不存在。')
  if (response.status === 409) {
    const body = await boundedConflictJSON(response) as { detail?: unknown } | null
    const detail = body?.detail
    const typed = detail && typeof detail === 'object' ? detail as { code?: unknown; message?: unknown } : null
    const message = sourceConflictDetail(typeof detail === 'string' ? detail :
      typed && typeof typed.code === 'string' && /^[a-z][a-z0-9_]{0,63}$/i.test(typed.code) ? typed.message : null)
    if (message) return new Error(message)
    return new Error('原件暂时无法读取或来源状态已改变（HTTP 409），请稍后重试。')
  }
  return new Error(`原件读取失败（HTTP ${response.status}）。`)
}
