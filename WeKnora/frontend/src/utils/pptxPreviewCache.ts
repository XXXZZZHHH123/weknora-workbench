/** Browser-memory preview data only. Object URLs belong to viewers, not this cache.
 * Callers must revalidate source permission/version before a cache lookup.
 */
export function getPptxPreviewScope(): string {
  try {
    const token = localStorage.getItem('weknora_token') || ''
    if (!token) return ''
    const home = JSON.parse(localStorage.getItem('weknora_tenant') || '{}')
    const tenant = localStorage.getItem('weknora_selected_tenant_id') || String(home.id || '')
    const base = (import.meta.env?.BASE_URL || '/').replace(/\/+$/, '')
    return JSON.stringify([globalThis.location?.origin || '', base, tenant, token])
  } catch { return '' }
}

type Loaded<T> = { value: T; bytes: number; cacheable?: boolean }
type Entry = Loaded<unknown> & { expires: number }
type Pending = { controller: AbortController; promise: Promise<unknown> }

export function createPptxPreviewCache(options: {
  getScope: () => string
  now?: () => number
  maxBytes?: number
  maxEntries?: number
  ttlMs?: number
}) {
  const now = options.now || Date.now
  const maxBytes = options.maxBytes ?? 64 * 1024 * 1024
  const maxEntries = options.maxEntries ?? 128
  const ttl = options.ttlMs ?? 10 * 60 * 1000
  const entries = new Map<string, Entry>()
  const inflight = new Map<string, Pending>()
  let bytes = 0
  let scope = ''
  let generation = 0
  function clear() {
    generation++
    for (const pending of inflight.values()) pending.controller.abort()
    inflight.clear()
    entries.clear()
    bytes = 0
  }
  function valid(requestScope: string) {
    const current = options.getScope()
    if (current !== scope) { clear(); scope = current }
    return !!requestScope && requestScope === current
  }
  function remove(key: string) {
    const entry = entries.get(key)
    if (entry) bytes -= entry.bytes
    entries.delete(key)
  }
  function prune() {
    for (const [key, entry] of entries) if (entry.expires <= now()) remove(key)
    while (bytes > maxBytes || entries.size > maxEntries) remove(entries.keys().next().value!)
  }
  function read<T>(requestScope: string, key: string): T | undefined {
    if (!valid(requestScope)) return undefined
    prune()
    const entry = entries.get(key)
    if (!entry) return undefined
    entries.delete(key); entries.set(key, entry)
    return entry.value as T
  }
  function getOrLoad<T>(requestScope: string, key: string,
    loader: (signal: AbortSignal) => Promise<Loaded<T>>): Promise<T> {
    if (!valid(requestScope)) return Promise.reject(new Error('Preview access changed'))
    const hit = read<T>(requestScope, key)
    if (hit !== undefined) return Promise.resolve(hit)
    const pending = inflight.get(key)
    if (pending) return pending.promise as Promise<T>
    const epoch = generation
    const controller = new AbortController()
    const task = {} as Pending
    task.controller = controller
    task.promise = Promise.resolve().then(() => {
      if (!valid(requestScope) || generation !== epoch) throw new Error('Preview access changed')
      return loader(controller.signal)
    }).then(loaded => {
      if (!valid(requestScope) || generation !== epoch || controller.signal.aborted) throw new Error('Preview access changed')
      if (!Number.isSafeInteger(loaded.bytes) || loaded.bytes < 0) throw new Error('Invalid preview cache size')
      // Oversized successes are usable by this viewer, but never evict all
      // smaller previews just to retain a single large presentation.
      if (loaded.cacheable !== false && loaded.bytes <= maxBytes && maxEntries > 0 && ttl > 0) {
        remove(key)
        entries.set(key, { ...loaded, expires: now() + ttl })
        bytes += loaded.bytes
        prune()
      }
      return loaded.value
    }).finally(() => { if (inflight.get(key) === task) inflight.delete(key) })
    inflight.set(key, task)
    return task.promise as Promise<T>
  }
  function invalidate(requestScope: string, key: string) {
    if (valid(requestScope)) remove(key)
  }
  return { read, getOrLoad, invalidate, clear }
}

export const pptxPreviewCache = createPptxPreviewCache({ getScope: getPptxPreviewScope })
