<template>
  <div class="structured-workspace">
    <div class="structured-toolbar"><div><strong>表格分析</strong><span>完整数值、字段与原单元格</span></div><div class="toolbar-actions"><router-link to="/platform/materials?add=1">添加资料</router-link><router-link :to="{path:'/platform/structured-data',query:route.query.view ? {} : {view:'model-settings'}}">{{ route.query.view ? '返回工作台' : '字段模型设置' }}</router-link></div></div>
    <iframe ref="frame" :key="src" :src="src" @load="sendAuth" title="结构化数据工作台" />
  </div>
</template>
<script setup lang="ts">
import {computed,onMounted,onBeforeUnmount,ref,watch} from 'vue'
import {useRoute,useRouter} from 'vue-router'
import {useAuthStore} from '@/stores/auth'
import { refreshStructuredFrame } from '@/utils/authFetch'
import {useTheme} from '@/composables/useTheme'
const auth=useAuthStore(),route=useRoute(),router=useRouter(),frame=ref<HTMLIFrameElement>(),generation=ref(0)
const {effectiveTheme}=useTheme()
const src=computed(()=>'/structured/?session='+generation.value+(route.query.view==='model-settings'?'&view=model-settings':'')+(typeof route.query.dataset==='string'?'&dataset='+encodeURIComponent(route.query.dataset):''))
function sendAuth(){frame.value?.contentWindow?.postMessage({type:'structured-auth',token:auth.token,tenant:auth.selectedTenantId??auth.tenant?.id},window.location.origin)}
function sendTheme(){frame.value?.contentWindow?.postMessage({type:'workspace-theme',mode:effectiveTheme.value},window.location.origin)}
function receive(e:MessageEvent){
  if(e.origin!==window.location.origin||e.source!==frame.value?.contentWindow)return
  if (e.data?.type === 'structured-refresh-auth') { void refreshStructuredFrame(e, () => frame.value?.contentWindow === e.source); return }
  if(e.data?.type==='workspace-theme-ready'){sendTheme();return}
  if(e.data?.type==='structured-ready')sendAuth()
  if(e.data?.type==='structured-navigate') {
    if(e.data.view==='materials')router.push({path:'/platform/materials',query:{add:'1'}})
    else if(e.data.view==='conversation')router.push('/platform/creatChat')
    else router.push({path:'/platform/structured-data',query:e.data.view==='model-settings'?{view:'model-settings'}:{}})
  }
}
watch(()=>[Boolean(auth.token),auth.selectedTenantId,auth.tenant?.id,auth.user?.id],()=>{generation.value+=1},{flush:'sync'})
watch(()=>auth.token,sendAuth)
watch(effectiveTheme,sendTheme)
onMounted(()=>window.addEventListener('message',receive))
onBeforeUnmount(()=>window.removeEventListener('message',receive))
</script>
<style scoped>
.structured-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--app-bg)}
.structured-toolbar{display:flex;flex-wrap:wrap;gap:12px;justify-content:space-between;align-items:center;padding:16px 24px;background:var(--app-card-bg);border-bottom:1px solid var(--app-border-color)}
.structured-toolbar>div{display:flex;align-items:center;gap:16px;min-width:0;flex-wrap:wrap}.structured-toolbar strong{font-weight:600;letter-spacing:-.015em}.structured-toolbar .toolbar-actions{gap:8px}.structured-toolbar a{padding:7px 10px;border-radius: var(--app-radius-md);transition:background-color var(--app-motion-fast) ease}.structured-toolbar a:hover{background:var(--td-brand-color-light)}.structured-toolbar span{font-size:var(--app-text-md);color:var(--app-text-secondary)}.structured-toolbar a{color:var(--td-brand-color);font-size:var(--app-text-md);text-decoration:none}iframe{width:100%;flex:1;min-height:0;border:0}
@media(max-width:600px){.structured-toolbar{padding:12px 16px}.structured-toolbar>div{gap:8px}.structured-toolbar span{width:100%;line-height:1.6}.toolbar-actions{width:100%}}
@media(prefers-reduced-motion:reduce){.structured-toolbar a{transition:none}}
</style>
