const messageKeys = new Map([
  ['Invalid login parameters', 'displayError.loginFields'],
  ['Email and password are required', 'displayError.loginFields'],
  ['Login failed', 'error.invalidCredentials'],
  ['token is required', 'displayError.invitationIncomplete'],
  ['invitation link is invalid or has been revoked', 'inviteRegister.invalidBody'],
  ['Invalid registration parameters', 'displayError.registerFields'],
  ['token, email, username and password are required', 'displayError.registerFields'],
  ['Username, email and password are required', 'displayError.registerFields'],
  ['Public registration is disabled', 'displayError.registerDisabled'],
  ['user with this email already exists', 'displayError.emailRegistered'],
  ['user with this username already exists', 'displayError.usernameRegistered'],
  ['Registration is disabled', 'displayError.registerDisabled'],
  ['this email already has an account; please log in to join the workspace', 'displayError.emailRegistered'],
  ['文档 ID 不能为空', 'file.downloadFailed'],
  ['Bucket 名称格式不正确，仅允许字母、数字、点、连字符', 'displayError.readStorage'],
  ['Endpoint、Access Key、Secret Key 不能为空', 'displayError.storageCredentials'],
  ['Access Key 与 Secret Key 必须同时填写或同时留空（使用 AWS 默认凭证链）', 'displayError.storageCredentialsPair'],
  ['认证失败，请检查静态密钥或 AWS IAM Role / 默认凭证链权限', 'displayError.storageRole'],
  ['tag_id 必须是整数', 'displayError.selectTag'],
  ['entry_id 必须是整数', 'displayError.selectFaq'],
  ['需要有效的主平台成员身份和相应空间权限', 'displayError.structuredAccess'],
  ['结构化模型服务尚未配置', 'displayError.structuredSetup'],
  ['知识库 ID 无效', 'displayError.structuredLibrary'],
  ['请选择有效模型并提供限定范围的对话参数', 'displayError.structuredModel'],
  ['PDF处理设置只能包含一个对象', 'pdfProcessing.failed'],
  ['保存PDF处理设置失败', 'pdfProcessing.failed'],
  ['当前研究来源超过128份表格原件容量，请拆分来源后重试；未静默截断数据', 'displayError.tooManyTableSources'],
  ['结构化导入记录的SHA256与原件不符', 'displayError.sourceVersion'],
  ['重复文件原件无法核验为本次SHA256版本；未关联错误文件', 'displayError.sourceVersion'],
  ['原件缺失或SHA256不符，未提供替代文件', 'displayError.sourceVersion'],
  ['结构化服务未返回后台任务号；原件已保留，可重试', 'displayError.processingUnconfirmed'],
  ['桥接需要同时提供真实 dataset_id 和 series_id', 'displayError.selectColumn'],
])

export function userFacingErrorKey(detail: unknown, fallback: string): string {
  return typeof detail === 'string' ? messageKeys.get(detail.trim()) ?? fallback : fallback
}

export function httpErrorTextKey(
  detail: unknown,
  status: number,
  method = 'GET',
  path = '',
  isTextResponse = false,
): string | undefined {
  const known = userFacingErrorKey(detail, '')
  if (known) return known
  const route = path.split('?')[0]
  if (route.includes('/auth/oidc/')) return 'auth.oidcLoginFailed'
  if (route.endsWith('/auth/login')) return status === 401 ? 'error.invalidCredentials' : 'auth.loginErrorRetry'
  if (route.endsWith('/auth/register') || route.endsWith('/auth/register-by-invite')) {
    if (status === 409) return 'displayError.emailRegistered'
    if (status === 410) return 'inviteRegister.invalidBody'
    return 'auth.registerError'
  }
  if (route.endsWith('/auth/invitations/lookup')) return 'inviteRegister.invalidBody'
  if (route.endsWith('/pdf-processing-settings')) return 'pdfProcessing.failed'
  if (status >= 500 || isTextResponse || typeof detail !== 'string') {
    return ['GET', 'HEAD'].includes(method.toUpperCase()) ? 'displayError.readFailed' : 'displayError.writeUnconfirmed'
  }
}
