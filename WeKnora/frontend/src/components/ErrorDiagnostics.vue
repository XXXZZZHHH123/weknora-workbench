<script setup lang="ts">
import { computed } from 'vue'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ detail: unknown }>()
const auth = useAuthStore()
const canView = computed(() => auth.isSystemAdmin || auth.hasRole('admin'))
const text = computed(() => {
  if (!canView.value || !props.detail) return ''
  return typeof props.detail === 'string' ? props.detail : JSON.stringify(props.detail, null, 2)
})
</script>

<template>
  <details v-if="text" class="error-diagnostics">
    <summary>{{ $t('displayError.diagnostics') }}</summary>
    <pre>{{ text }}</pre>
  </details>
</template>

<style scoped>
.error-diagnostics pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
