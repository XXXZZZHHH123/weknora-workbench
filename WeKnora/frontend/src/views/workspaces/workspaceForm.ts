export function validateWorkspaceName(raw: string): string {
  const name = raw.trim()
  if (!name) return '请填写名称。'
  if (Array.from(name).length > 80) return '名称不能超过 80 个字符。'
  if (/[\u0000-\u001f\u007f]/.test(name)) return '名称不能包含控制字符。'
  return ''
}

export function validateWorkbenchSource(id: string, accessibleIds: string[]): string {
  return id && accessibleIds.includes(id) ? '' : '请选择一个有权限的主知识库。'
}

export function workspaceIdentity(tenant: string | number | undefined, user: string | undefined): string {
  return `${tenant ?? ''}:${user ?? ''}`
}

export function workspaceErrorMessage(error: unknown): string {
  if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') return error.message
  return '请求失败，请重试。'
}
