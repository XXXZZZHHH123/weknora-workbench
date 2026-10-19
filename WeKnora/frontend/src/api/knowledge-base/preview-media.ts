import { get } from '@/utils/request'

/** Fetch one server-rasterized media part from an authorized source file. */
export function previewKnowledgeMedia(
  knowledgeId: string,
  physicalPage: number,
  part: string,
  sourceSha256: string,
  signal?: AbortSignal,
): Promise<Blob> {
  if (!knowledgeId.trim()) return Promise.reject(new Error('Knowledge ID is required'))
  if (!Number.isInteger(physicalPage) || physicalPage < 1) return Promise.reject(new Error('Invalid physical page'))
  if (!/^ppt\/media\/[^/\\?#]+\.(?:emf|wmf)$/i.test(part)) {
    return Promise.reject(new Error('Invalid vector media part'))
  }
  if (!/^[a-f0-9]{64}$/i.test(sourceSha256)) return Promise.reject(new Error('Invalid source fingerprint'))
  const query = new URLSearchParams({ page: String(physicalPage), part, source_sha256: sourceSha256.toLowerCase() })
  return get<Blob>(`/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/preview-media?${query}`, {
    responseType: 'blob',
    timeout: 45_000,
    signal,
  }) as unknown as Promise<Blob>
}

