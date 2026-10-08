<template>
  <div class="structured-workspace">
    <div class="structured-toolbar"><strong>结构化数据</strong><router-link :to="{path:'/platform/structured-data',query:route.query.view ? {} : {view:'model-settings'}}">{{ route.query.view ? '返回工作台' : 'AI 模型设置' }}</router-link></div>
    <iframe ref="frame" :key="src" :src="src" @load="sendAuth" title="结构化数据工作台" />
  </div>
</template>
<script setup lang="ts">
import {computed,onMounted,onBeforeUnmount,ref,watch} from 'vue'
import {useRoute,useRouter} from 'vue-router'
import {useAuthStore} from '@/stores/auth'
const auth=useAuthStore(),route=useRoute(),router=useRouter(),frame=ref<HTMLIFrameElement>()
const src=computed(()=>'/structured/'+(route.query.view==='model-settings'?'?view=model-settings':''))
function sendAuth(){frame.value?.contentWindow?.postMessage({type:'structured-auth',token:auth.token,tenant:auth.selectedTenantId??auth.tenant?.id},window.location.origin)}
function receive(e:MessageEvent){
  if(e.origin!==window.location.origin||e.source!==frame.value?.contentWindow)return
  if(e.data?.type==='structured-ready')sendAuth()
  if(e.data?.type==='structured-navigate')router.push({path:'/platform/structured-data',query:e.data.view==='model-settings'?{view:'model-settings'}:{}})
}
watch(()=>[auth.token,auth.selectedTenantId,auth.tenant?.id],sendAuth)
onMounted(()=>window.addEventListener('message',receive))
onBeforeUnmount(()=>window.removeEventListener('message',receive))
</script>
<style scoped>
.structured-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:#f4f7f5}
.structured-toolbar{display:flex;justify-content:space-between;align-items:center;padding:16px 24px;background:white;border-bottom:1px solid #dce5df}
.structured-toolbar a{color:#176750}iframe{width:100%;flex:1;min-height:0;border:0}
</style>
