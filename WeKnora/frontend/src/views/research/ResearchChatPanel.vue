<template>
  <aside v-show="visible" id="research-chat-panel" class="research-chat-panel" aria-label="研究工作台问答助手">
    <div class="research-chat-panel__header">
      <strong>问答助手</strong>
      <t-button variant="text" size="small" aria-label="收起问答助手" @click="$emit('close')">
        <template #icon><t-icon name="chevron-right-double" /></template>
        收起
      </t-button>
    </div>
    <div class="research-chat-panel__context">
      <p>知识库：{{ kbName || kbId || '未绑定' }}</p>
      <p>指标来自绑定知识库；研究成果按当前工作台和账号保存。</p>
    </div>
    <div v-if="chat.state.loading" class="research-chat-panel__status" role="status">正在打开对话…</div>
    <div v-else-if="chat.state.error" class="research-chat-panel__status" role="alert">
      <p>{{ chat.state.error }}</p>
      <t-button variant="outline" @click="chat.open">重试</t-button>
    </div>
    <ChatView v-else-if="chat.state.sessionId" :key="chat.state.sessionId" :session_id="chat.state.sessionId"
      embedded-mode scoped-session :scoped-model-id="modelId" :kb-ids="kbId ? [kbId] : []"
      agent-id="builtin-quick-answer" />
  </aside>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import ChatView from '@/views/chat/index.vue'
import { createSessions } from '@/api/chat'
import { useMenuStore } from '@/stores/menu'
import { useSettingsStore } from '@/stores/settings'
import { createResearchChatSession } from './researchChatSession'

const props = withDefaults(defineProps<{ visible: boolean; kbId: string; kbName: string; workbenchName: string; isolated?: boolean; workbenchId?: string }>(), { isolated: false, workbenchId: '' })
defineEmits<{ close: [] }>()
const settings = useSettingsStore()
const menu = useMenuStore()
const modelId = ref(settings.conversationModels.selectedChatModelId || settings.conversationModels.summaryModelId || '')
let disposed = false

const chat = createResearchChatSession(async () => {
  const title = `${props.workbenchName || '研究工作台'} · 问答`
  if (!props.workbenchId || !props.kbId) throw new Error('请重新打开当前工作台后重试。')
  const response = await createSessions({
    title,
    research_workbench_id: props.workbenchId,
    agent_config: {
      enabled: false,
      knowledge_bases: props.kbId ? [props.kbId] : [],
    },
  }) as { data?: { id?: string }; message?: string }
  const id = response?.data?.id
  if (!id) throw new Error(response?.message || '未能创建对话，请重试。')
  if (!disposed) {
    const now = new Date().toISOString()
    menu.updataMenuChildren({ id, title, path: `chat/${id}`, isMore: false, isNoTitle: false, created_at: now, updated_at: now })
  }
  return id
})

watch(() => props.visible, (visible) => { if (visible) void chat.open(); else chat.close() }, { immediate: true })
onBeforeUnmount(() => { disposed = true; chat.dispose() })
</script>

<style scoped>
.research-chat-panel{display:flex;flex-direction:column;flex:0 0 400px;width:400px;min-width:0;min-height:0;background:var(--app-card-bg,white);border-left:1px solid var(--app-border-color,#dce5df);overflow:hidden}
.research-chat-panel__header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:14px 16px;border-bottom:1px solid var(--app-border-color,#dce5df)}
.research-chat-panel__header strong{font-size:var(--app-text-md);color:var(--app-text-primary)}
.research-chat-panel__context{padding:10px 16px;border-bottom:1px solid var(--app-border-color,#dce5df);font-size:var(--app-text-sm);color:var(--app-text-secondary)}
.research-chat-panel__context p{margin:0 0 8px;line-height:1.6;overflow-wrap:anywhere}
.research-chat-panel__status{padding:24px 16px;line-height:1.6;overflow-wrap:anywhere}
@media(max-width:900px){.research-chat-panel{position:absolute;inset:0 0 0 auto;width:min(400px,100%);z-index:5;box-shadow:var(--td-shadow-2)}}
</style>
