export type StructuredFrameSourceType = 'pdf' | 'xlsx'

export interface StructuredFrameAuthContext {
  token: string
  tenant: string | number
  knowledgeBaseId: string
  sourceKnowledgeId: string
  sourceFileType: StructuredFrameSourceType
}

export function structuredFrameAuthPayload(context: StructuredFrameAuthContext) {
  return { type: 'structured-auth', ...context }
}

export function isTrustedStructuredFrameMessage(
  event: Pick<MessageEvent, 'origin' | 'source'>,
  frame: HTMLIFrameElement | undefined,
  origin: string,
): boolean {
  return Boolean(frame?.contentWindow && event.origin === origin && event.source === frame.contentWindow)
}
