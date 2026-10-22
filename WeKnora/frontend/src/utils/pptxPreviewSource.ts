/** Only a freshly authorized metadata response can select a shared preview. */
export function knowledgePptxSourceKey(id: string, metadata: unknown): string | null {
  if (!metadata || typeof metadata !== 'object') return null
  const item = metadata as Record<string, unknown>
  if (!id || item.id !== id || item.deleted_at || typeof item.file_path !== 'string' || !item.file_path.trim()
    || typeof item.file_hash !== 'string' || !item.file_hash.trim()
    || !Number.isSafeInteger(item.file_size) || (item.file_size as number) < 0
    || typeof item.updated_at !== 'string' || !item.updated_at.trim()) return null
  return JSON.stringify([id, item.file_path, item.file_hash, item.file_size, item.updated_at])
}
