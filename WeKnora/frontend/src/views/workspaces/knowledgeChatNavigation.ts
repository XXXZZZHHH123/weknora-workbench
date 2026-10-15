export function conversationPath(sessionId: string, knowledgeBaseId?: unknown): string {
  const conversation = encodeURIComponent(sessionId)
  return typeof knowledgeBaseId === 'string' && knowledgeBaseId
    ? `/platform/knowledge-bases/${encodeURIComponent(knowledgeBaseId)}/chat/${conversation}`
    : `/platform/chat/${conversation}`
}
