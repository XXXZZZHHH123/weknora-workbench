<template>
  <aside v-show="visible" id="research-chat-panel" class="research-chat-panel" aria-label="研究工作台问答助手">
    <div class="research-chat-panel__header">
      <strong>问答助手</strong>
      <t-button variant="text" size="small" aria-label="收起问答助手" @click="$emit('close')">
        <template #icon><t-icon name="chevron-right-double" /></template>
        收起
      </t-button>
    </div>
    <div v-if="chat.state.loading" class="research-chat-panel__status" role="status">正在打开对话…</div>
    <div v-else-if="chat.state.error" class="research-chat-panel__status" role="alert">
      <p>{{ chat.state.error }}</p>
      <t-button variant="outline" @click="chat.open">重试</t-button>
    </div>
    <ChatView v-else-if="chat.state.sessionId" :key="chat.state.sessionId" :session_id="chat.state.sessionId"
      embedded-mode scoped-session :scoped-model-id="modelId" :scoped-kb-name="kbName" :kb-ids="sourceIds"
      agent-id="builtin-quick-answer" />
  </aside>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import ChatView from '@/views/chat/index.vue'
import { createSessions } from '@/api/chat'
import { useSettingsStore } from '@/stores/settings'
import { createResearchChatSession } from './researchChatSession'

const props = withDefaults(defineProps<{ visible: boolean; kbId: string; kbIds?: string[]; kbName: string; workbenchName: string; isolated?: boolean; workbenchId?: string }>(), { isolated: false, workbenchId: '' })
defineEmits<{ close: [] }>()
const settings = useSettingsStore()
const modelId = ref(settings.conversationModels.selectedChatModelId || settings.conversationModels.summaryModelId || '')
const sourceIds = computed(() => props.kbIds ?? (props.kbId ? [props.kbId] : []))

const chat = createResearchChatSession(async () => {
  const title = `${props.workbenchName || '研究工作台'} · 问答`
  if (!props.workbenchId) throw new Error('请重新打开当前工作台后重试。')
  const response = await createSessions({
    title,
    research_workbench_id: props.workbenchId,
    agent_config: {
      enabled: false,
      knowledge_bases: sourceIds.value,
    },
  }) as { data?: { id?: string }; message?: string }
  const id = response?.data?.id
  if (!id) throw new Error(response?.message || '未能创建对话，请重试。')
  return id
})

watch(() => props.visible, (visible) => { if (visible) void chat.open(); else chat.close() }, { immediate: true })
onBeforeUnmount(() => chat.dispose())
</script>

<style scoped>
.research-chat-panel{display:flex;flex-direction:column;flex:0 0 400px;width:400px;min-width:0;min-height:0;background:var(--app-card-bg,white);border-left:1px solid var(--app-border-color,#dce5df);overflow:hidden}
.research-chat-panel__header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:14px 16px;border-bottom:1px solid var(--app-border-color,#dce5df)}
.research-chat-panel__header strong{font-size:var(--app-text-md);color:var(--app-text-primary)}
.research-chat-panel__status{padding:24px 16px;line-height:1.6;overflow-wrap:anywhere}
@media(max-width:900px){.research-chat-panel{position:absolute;inset:0 0 0 auto;width:min(400px,100%);z-index:5;box-shadow:var(--td-shadow-2)}}
</style>
