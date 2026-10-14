import { reactive } from 'vue'

export interface ResearchChatSessionState {
  visible: boolean
  sessionId: string
  loading: boolean
  error: string
}

// Hiding the panel must keep its in-flight creation and conversation intact.
// Disposal, in contrast, prevents a previous identity's response from landing.
export function createResearchChatSession(create: () => Promise<string>) {
  const state = reactive<ResearchChatSessionState>({ visible: false, sessionId: '', loading: false, error: '' })
  let disposed = false
  let pending: Promise<void> | null = null

  async function open() {
    if (disposed) return
    state.visible = true
    if (state.sessionId) return
    if (pending) return pending
    state.loading = true
    state.error = ''
    pending = (async () => {
      try {
        const id = await create()
        if (!id) throw new Error('未能创建对话，请重试。')
        if (!disposed) state.sessionId = id
      } catch (error) {
        if (!disposed) state.error = error instanceof Error ? error.message : '未能创建对话，请重试。'
      } finally {
        if (!disposed) state.loading = false
      }
    })().finally(() => { pending = null })
    return pending
  }

  function close() { state.visible = false }
  function dispose() {
    disposed = true
    state.visible = false
    state.sessionId = ''
    state.loading = false
    state.error = ''
  }

  return { state, open, close, dispose }
}
