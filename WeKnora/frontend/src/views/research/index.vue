<template>
  <div class="research-workspace">
    <div class="research-toolbar"><strong>商品研究</strong><span>当前空间的研究数据与个人判断</span></div>
    <iframe v-if="auth.token && tenantId" :key="generation" ref="frame" :src="bootstrapUrl"
      title="商品研究工作台" @load="sendAuth" />
    <div v-else class="research-empty">请先选择空间。</div>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
const auth = useAuthStore()
const frame = ref<HTMLIFrameElement>()
const generation = ref(0)
const tenantId = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const bootstrapUrl = computed(() => `/research/auth/bootstrap?session=${generation.value}`)
function restartSession() { generation.value += 1 }
function sendAuth() {
  if (!auth.token || !tenantId.value) return
  frame.value?.contentWindow?.postMessage({
    type: 'research-auth', token: auth.token, tenantId: tenantId.value,
  }, window.location.origin)
}
function receive(event: MessageEvent) {
  if (event.origin !== window.location.origin || event.source !== frame.value?.contentWindow) return
  if (event.data?.type === 'research-ready') sendAuth()
  if (event.data?.type === 'research-auth-expired') restartSession()
}
// Replace the whole iframe on identity changes; old workspace content must disappear.
watch(() => [auth.token, tenantId.value, auth.user?.id], restartSession, { flush: 'sync' })
onMounted(() => window.addEventListener('message', receive))
onBeforeUnmount(() => window.removeEventListener('message', receive))
</script>
<style scoped>
.research-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--app-bg,#f4f7f5)}
.research-toolbar{display:flex;gap:24px;align-items:center;padding:16px 24px;background:var(--app-card-bg,white);border-bottom:1px solid var(--app-border-color,#dce5df)}
.research-toolbar span{font-size:13px;color:var(--app-text-secondary,#5b6b63)}
.research-empty{padding:24px}iframe{width:100%;flex:1;min-height:0;border:0}
</style>
