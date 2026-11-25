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

export class StructuredSourceReadError extends Error {
  constructor(message: string, public readonly diagnostic = '') {
    super(message)
  }
}

/** Keep the response text for diagnostics. Only display recognized causes. */
export async function structuredSourceReadError(
  response: Response,
  translate: (key: string, fallback: string) => string = (_, fallback) => fallback,
): Promise<StructuredSourceReadError> {
  const status = `HTTP ${response.status}`
  if (response.status === 401) return new StructuredSourceReadError(translate('structuredReview.sourceExpired', '登录已失效，请重新登录后继续。'), status)
  if (response.status === 403) return new StructuredSourceReadError(translate('structuredReview.sourceDenied', '当前账号无权读取原件，请联系资料负责人。'), status)
  if (response.status === 404) return new StructuredSourceReadError(translate('structuredReview.sourceMissing', '这份原件已不存在或暂时无法找到，请重新选择来源。'), status)
  if (response.status === 409) {
    const body = await boundedConflictJSON(response) as { detail?: unknown } | null
    const detail = body?.detail
    const typed = detail && typeof detail === 'object' ? detail as { code?: unknown; message?: unknown } : null
    const diagnostic = sourceConflictDetail(typeof detail === 'string' ? detail : typed?.message)
    if (detail === '正在读取原表，请稍后再查看其他区域。' || detail === '正在读取原表，请稍后再查看样本。') {
      const key = detail === '正在读取原表，请稍后再查看样本。' ? 'sampleBusy' : 'sourceBusy'
      return new StructuredSourceReadError(translate(`structuredReview.${key}`, detail), [status, detail].join(' · '))
    }
    if (typed?.code === 'source_changed') {
      return new StructuredSourceReadError(translate('structuredReview.sourceChanged', '原件版本已变化，请重新读取。'), [status, diagnostic].filter(Boolean).join(' · '))
    }
    return new StructuredSourceReadError(translate('structuredReview.sourceConflict', '原件暂时无法读取或来源已变化，请重新选择来源后再试。'), [status, diagnostic].filter(Boolean).join(' · '))
  }
  return new StructuredSourceReadError(translate('structuredReview.readFailed', '暂时无法读取原件，请稍后重试。'), status)
}
