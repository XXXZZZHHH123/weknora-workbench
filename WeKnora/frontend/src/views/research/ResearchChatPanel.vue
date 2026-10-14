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
      <p>{{ kbId ? '问答使用当前绑定知识库；' : '' }}研究数据与个人成果沿用当前空间。</p>
      <label class="research-chat-panel__model">
        <span>模型</span>
        <ModelSelector model-type="KnowledgeQA" v-model:selected-model-id="modelId"
          :disabled="replying" @add-model="ui.openSettings('models')" />
      </label>
    </div>
    <div v-if="chat.state.loading" class="research-chat-panel__status" role="status">正在打开对话…</div>
    <div v-else-if="chat.state.error" class="research-chat-panel__status" role="alert">
      <p>{{ chat.state.error }}</p>
      <t-button variant="outline" @click="chat.open">重试</t-button>
    </div>
    <ChatView v-else-if="chat.state.sessionId" :key="chat.state.sessionId" :session_id="chat.state.sessionId"
      embedded-mode scoped-session :scoped-model-id="modelId" :kb-ids="kbId ? [kbId] : []"
      :agent-id="agentId" :agent-source-tenant-id="agentSourceTenantId"
      @replying-change="replying = $event" />
  </aside>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import ChatView from '@/views/chat/index.vue'
import ModelSelector from '@/components/ModelSelector.vue'
import { createSessions } from '@/api/chat'
import { useMenuStore } from '@/stores/menu'
import { useSettingsStore } from '@/stores/settings'
import { useUIStore } from '@/stores/ui'
import { createResearchChatSession } from './researchChatSession'

const props = defineProps<{ visible: boolean; kbId: string; kbName: string; workbenchName: string }>()
defineEmits<{ close: [] }>()
const settings = useSettingsStore()
const menu = useMenuStore()
const ui = useUIStore()
const modelId = ref(settings.conversationModels.selectedChatModelId || settings.conversationModels.summaryModelId || '')
const agentId = settings.selectedAgentId
const agentSourceTenantId = settings.selectedAgentSourceTenantId || undefined
const replying = ref(false)
let disposed = false

const chat = createResearchChatSession(async () => {
  const title = `${props.workbenchName || '研究工作台'} · 问答`
  const response = await createSessions({
    title,
    agent_config: {
      enabled: settings.isAgentStreamMode,
      max_iterations: settings.agentConfig.maxIterations,
      temperature: settings.agentConfig.temperature,
      allowed_tools: settings.agentConfig.allowedTools,
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
.research-chat-panel__model{display:flex;align-items:center;gap:12px}
.research-chat-panel__model>span{flex:none}
.research-chat-panel__model :deep(.model-selector){flex:1;min-width:0}
.research-chat-panel__status{padding:24px 16px;line-height:1.6;overflow-wrap:anywhere}
@media(max-width:900px){.research-chat-panel{position:absolute;inset:0 0 0 auto;width:min(400px,100%);z-index:5;box-shadow:var(--td-shadow-2)}}
</style>
