/** Only persisted message artifacts can become generated-image previews. */
export interface GeneratedImageArtifact {
  index: number
  file_name: string
  file_type: string
  file_size: number
  handle?: string
}

export interface GeneratedImageDelivery {
  sessionId: string
  messageId: string
  artifacts: GeneratedImageArtifact[]
}

export function generatedImageDelivery(data: Record<string, unknown>): GeneratedImageDelivery | null {
  const sessionId = typeof data.session_id === 'string' ? data.session_id.trim() : ''
  const messageId = typeof data.message_id === 'string' ? data.message_id.trim() : ''
  if (!/^[A-Za-z0-9_-]+$/.test(sessionId) || !/^[A-Za-z0-9_-]+$/.test(messageId)) return null
  const artifacts = (Array.isArray(data.artifacts) ? data.artifacts : []).flatMap((raw): GeneratedImageArtifact[] => {
    if (!raw || typeof raw !== 'object') return []
    const a = raw as Record<string, unknown>
    if (!Number.isInteger(a.index) || Number(a.index) < 0 || typeof a.file_name !== 'string' || !a.file_name) return []
    return [{
      index: Number(a.index), file_name: a.file_name,
      file_type: typeof a.file_type === 'string' ? a.file_type : '',
      file_size: typeof a.file_size === 'number' ? a.file_size : 0,
      ...(typeof a.handle === 'string' ? { handle: a.handle } : {}),
    }]
  })
  return artifacts.length ? { sessionId, messageId, artifacts } : null
}

export async function loadGeneratedImage(
  delivery: GeneratedImageDelivery,
  artifact: GeneratedImageArtifact,
  download: (sessionId: string, messageId: string, index: number) => Promise<Blob>,
): Promise<Blob> {
  const blob = await download(delivery.sessionId, delivery.messageId, artifact.index)
  if (!blob.size || blob.size > 32 * 1024 * 1024 || !/^image\/(png|jpeg|webp|gif|avif|bmp)$/i.test(blob.type)) {
    throw new Error('Invalid saved image')
  }
  return blob
}
