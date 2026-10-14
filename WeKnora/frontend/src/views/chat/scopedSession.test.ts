import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const between = (start: string, end: string) => {
  const index = source.indexOf(start)
  const finish = source.indexOf(end, index)
  assert.ok(index >= 0 && finish > index)
  return source.slice(index, finish)
}

test('a scoped panel mounts without consuming a pending full-page question or changing its settings', async () => {
  const callbacks: Array<() => Promise<void>> = []
  const history: unknown[] = []
  const query = { value: 'unsent full-page draft' }
  const fail = () => { throw new Error('panel touched global draft or settings') }
  const context = {
    props: { scopedSession: true }, firstQuery: query,
    useSettingsStoreInstance: new Proxy({}, { get: () => fail }),
    usemenuStore: { changeFirstQuery: fail }, sendMsg: fail,
    onBeforeMount: (fn: () => Promise<void>) => callbacks.push(fn),
    onMounted: (fn: () => Promise<void>) => callbacks.push(fn),
    window: { addEventListener() {} }, SESSION_MUTATION_EVENT: 'test', handleSessionMutation() {},
    messagesList: [], steerQueue: { value: [] }, loading: { value: false }, isReplying: { value: false },
    historyLoading: { value: true }, firstModelId: { value: '' }, firstMentionedItems: { value: [] },
    firstImageFiles: { value: [] }, firstAttachmentFiles: { value: [] }, firstQuestionOrigin: { value: null },
    scrollLock: { value: false }, hasMoreHistory: { value: false }, historyLoadingMore: { value: false },
    session_id: { value: 'panel-session' }, limit: { value: 20 },
    getmsgList: (request: unknown) => history.push(request),
  }
  runInNewContext(between('onBeforeMount(async () => {', 'const clearData ='), context)
  for (const callback of callbacks) await callback()
  assert.equal(query.value, 'unsent full-page draft')
  assert.equal(history.length, 1)
  assert.equal((history[0] as { session_id: string }).session_id, 'panel-session')
})

test('a full-page route change cannot replace the scoped conversation', async () => {
  let callback!: (value: unknown[]) => Promise<void>
  const conversation = { value: 'panel-session' }
  runInNewContext(between('watch([() => route.params]', 'const scrollToBottom ='), {
    props: { scopedSession: true }, session_id: conversation,
    watch: (_sources: unknown, fn: typeof callback) => { callback = fn },
  })
  await callback([{ chatid: 'other-conversation' }])
  assert.equal(conversation.value, 'panel-session')
})

test('panel submission keeps the complete question and sends its selected model, agent and real KB scope', async () => {
  let sent: any
  const question = '请结合所有可用证据说明变化、口径差异及仍不确定的部分。'
  const context = {
    props: { scopedSession: true, scopedModelId: 'panel-model', embeddedMode: true,
      kbIds: ['authorized-kb'], agentId: 'selected-agent', agentSourceTenantId: 'shared-source' },
    composerLocked: { value: false }, useSettingsStoreInstance: { selectedAgentId: 'global-agent',
      settings: { selectedKnowledgeBases: ['unrelated-kb'], selectedFiles: ['unrelated-file'] } },
    stopStream() {}, prepareForNewOutgoingMessage() {}, scrollToBottom() {},
    activitySessionId: { value: '' }, session_id: { value: 'panel-session' },
    isReplying: { value: false }, loading: { value: false }, userHasScrolledUp: { value: false },
    messagesList: [], pendingSuggestionKnowledgeBaseIds: [], pendingSuggestionAttribution: null,
    startStream: async (payload: unknown) => { sent = payload },
  }
  const send = runInNewContext(`${between('const sendMsg = async', '// Quietly recover')}\nsendMsg`, context)
  await send(question, 'global-model')
  assert.equal(sent.query, question)
  assert.equal(sent.summary_model_id, 'panel-model')
  assert.equal(sent.session_id, 'panel-session')
  assert.deepEqual(Array.from(sent.knowledge_base_ids), ['authorized-kb'])
  assert.deepEqual(Array.from(sent.knowledge_ids), [])
  assert.equal(sent.agent_id, 'selected-agent')
  assert.equal(sent.agent_source_tenant_id, 'shared-source')
  assert.equal(sent.url, '/api/v1/agent-chat')
})

test('a scoped composer without a selected model stays locked', () => {
  const code = between('const composerLocked = computed(', 'function stashForkLanding')
  const props = { scopedSession: true, scopedModelId: '' }
  const value = () => runInNewContext(`${code}\ncomposerLocked`, {
    props, computed: (fn: () => boolean) => fn(), rewindInFlight: { value: false },
  })
  assert.equal(value(), true)
  props.scopedModelId = 'explicit-model'
  assert.equal(value(), false)
})

test('changing research subpages keeps the panel stream and full-page defaults intact', () => {
  let callback!: (to: unknown, from: unknown, next: () => void) => void
  let nextCalled = false
  runInNewContext(between('onBeforeRouteUpdate((to, from, next)', '</script>'), {
    props: { scopedSession: true },
    onBeforeRouteUpdate: (fn: typeof callback) => { callback = fn },
    clearData: () => { throw new Error('unexpected stream reset') },
  })
  callback({}, {}, () => { nextCalled = true })
  assert.equal(nextCalled, true)
})

test('scoped stop cancels the authenticated session and reports server failure', async () => {
  for (const fail of [false, true]) {
    const effects: string[] = []
    const stop = runInNewContext(`${between('const handleScopedStop =', 'const dropSteerQueueItem =')}\nhandleScopedStop`, {
      session_id: { value: 'panel-session' }, currentAssistantMessageId: { value: 'reply-id' },
      handleStopGeneration: () => effects.push('local-stop'),
      handleStopConfirmed: () => effects.push('confirmed'),
      handleStopFailed: () => effects.push('failed'),
      stopSession: async (sid: string, mid: string) => {
        assert.equal(sid, 'panel-session'); assert.equal(mid, 'reply-id')
        effects.push('server-stop')
        if (fail) throw new Error('server unavailable')
      },
      MessagePlugin: { error: () => effects.push('error') }, t: (key: string) => key,
    })
    await stop()
    assert.deepEqual(effects, fail ? ['local-stop', 'server-stop', 'failed', 'error'] : ['local-stop', 'server-stop', 'confirmed'])
  }
})

test('scoped stop waits for a real assistant ID before stopping', async () => {
  const effects: string[] = []
  const stop = runInNewContext(`${between('const handleScopedStop =', 'const dropSteerQueueItem =')}\nhandleScopedStop`, {
    session_id: { value: 'panel-session' }, currentAssistantMessageId: { value: '' },
    MessagePlugin: { warning: () => effects.push('missing-id') }, t: (key: string) => key,
    stopSession: () => { throw new Error('cannot stop a fabricated message') },
  })
  await stop()
  assert.deepEqual(effects, ['missing-id'])
})
