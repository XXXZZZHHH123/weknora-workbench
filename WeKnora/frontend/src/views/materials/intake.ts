export type IntakeIdentity = { userId: string; tenantId: string }
export type IntakeContext = { kbId?: string; folder?: string; tagIds?: string[]; purpose?: string; url?: string }
type IntakeTransfer = { identity: IntakeIdentity; files: File[]; context: IntakeContext; expires: number }
let pending: IntakeTransfer | undefined

export function offerIntake(identity: IntakeIdentity, files: File[], context: IntakeContext = {}) {
  pending = { identity: { ...identity }, files: [...files], context: { ...context, tagIds: [...(context.tagIds || [])] }, expires: Date.now() + 10 * 60_000 }
}

export function takeIntake(identity: IntakeIdentity, now = Date.now()) {
  const transfer = pending
  pending = undefined
  if (!transfer || now > transfer.expires || transfer.identity.userId !== identity.userId || transfer.identity.tenantId !== identity.tenantId) return undefined
  return { files: transfer.files, context: transfer.context }
}

export function isTable(filename: string) { return /\.(xlsx|xlsm|xls|csv|tsv)$/i.test(filename) }
export function isStructuredFile(filename: string) { return /\.xlsx$/i.test(filename) }
export function fileKind(filename: string) { return isTable(filename) ? '表格' : '文档' }
export function readableSize(bytes: number) {
  return bytes >= 1024 * 1024 ? `${(bytes / (1024 * 1024)).toFixed(1)} MB` : `${Math.max(1, Math.round(bytes / 1024))} KB`
}
export const purposeNames: Record<string, string> = { knowledge: '知识库正文', structured: '表格分析', research: '研究指标' }
export const stateNames: Record<string, string> = { uploaded: '原件已保存', pending: '等待处理', running: '处理中', processing: '处理中', needs_mapping: '待确认', completed: '已就绪', failed: '处理失败', interrupted: '可恢复' }
export function stateName(status: string) { return stateNames[status] || '状态待核对' }
