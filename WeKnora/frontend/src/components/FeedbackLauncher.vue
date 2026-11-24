<template>
  <template v-if="enabled">
    <div ref="rail" class="feedback-dock" :class="{ 'is-dragging': dragging }"
      :data-task-placement="opensDown ? 'down' : 'up'" :style="positionStyle">
      <slot />
      <button ref="handle" type="button" class="feedback-launcher" :class="{ 'is-open': visible, 'is-raised': onChatRoute }"
        aria-label="反馈与建议" :aria-keyshortcuts="mobile ? undefined : 'Alt+ArrowUp Alt+ArrowDown'"
        @pointerdown="startDrag" @pointermove="moveDrag" @pointerup="finishDrag"
        @pointercancel="cancelDrag" @lostpointercapture="cancelDrag" @keydown="moveWithKeyboard"
        @click="click" @dblclick="reset">
        <t-icon name="chat-bubble-smile" size="18px" aria-hidden="true" />
        <span class="feedback-launcher__label" aria-hidden="true">反馈</span>
      </button>
    </div>
    <FeedbackDialog v-model:visible="visible" />
  </template>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useFeedbackDock } from '@/composables/useFeedbackDock'
import FeedbackDialog from './FeedbackDialog.vue'

const route = useRoute()
const authStore = useAuthStore()
const visible = ref(false)
const rail = ref<HTMLElement | null>(null)
const handle = ref<HTMLButtonElement | null>(null)

// 对话页底部是输入框，按钮上移避免挡住发送键。
const CHAT_ROUTES = new Set(['chat', 'kbChat', 'globalCreatChat', 'kbCreatChat'])
const onChatRoute = computed(() => CHAT_ROUTES.has(String(route.name)))
const enabled = computed(() => authStore.isLoggedIn && route.matched.some((r) => r.meta.requiresAuth))
const storageKey = computed(() => {
  const tenant = authStore.effectiveTenantId
  const user = authStore.user?.id
  return tenant && user ? `weknora:feedback-dock:v1:${encodeURIComponent(tenant)}:${encodeURIComponent(user)}` : null
})
const { mobile, dragging, positionStyle, opensDown, startDrag, moveDrag, finishDrag, cancelDrag, click, reset, moveWithKeyboard } =
  useFeedbackDock(rail, handle, storageKey, () => { visible.value = true })
</script>

<style lang="less" scoped>
.feedback-dock {
  --feedback-size: 42px;
  --feedback-bottom: 24px;
  --feedback-task-gap: 12px;
  --feedback-rail-min-height: 128px;
  --feedback-anchor-top: clamp(0px, var(--feedback-dock-top, 100%), calc(100% - var(--feedback-size)));
  position: fixed;
  top: 0;
  right: 24px;
  bottom: min(var(--feedback-bottom), max(0px, calc(100% - var(--feedback-rail-min-height))));
  width: var(--feedback-size);
  // 低于 TDesign 抽屉和弹窗，避免挡住其底部按钮。
  z-index: 1400;
  pointer-events: none;
}

.feedback-dock:has(.feedback-launcher.is-raised),
body:has([data-bottom-composer]) .feedback-dock {
  --feedback-bottom: 160px;
}

.feedback-launcher {
  position: absolute;
  top: var(--feedback-anchor-top);
  right: 0;
  display: grid;
  place-items: center;
  box-sizing: border-box;
  width: var(--feedback-size);
  height: var(--feedback-size);
  padding: 0;
  border: none;
  border-radius: 50%;
  background: var(--td-brand-color);
  color: var(--td-text-color-anti);
  box-shadow: var(--cds-shadow-lg);
  cursor: grab;
  touch-action: none;
  pointer-events: auto;
  transition: background-color var(--app-motion-base) ease, opacity var(--app-motion-base) ease;

  &:hover { background: var(--td-brand-color-hover); }
  &:active { background: var(--td-brand-color-active); }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 3px;
  }

  &.is-open {
    opacity: 0;
    pointer-events: none;
  }
}

.feedback-launcher__label {
  position: absolute;
  top: 50%;
  right: calc(100% + 8px);
  transform: translateY(-50%);
  padding: 4px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--td-bg-color-component);
  color: var(--td-text-color-primary);
  box-shadow: var(--td-shadow-2);
  font: inherit;
  white-space: nowrap;
  pointer-events: none;
  visibility: hidden;
}

.feedback-dock:not(.is-dragging) .feedback-launcher:is(:hover, :focus-visible) .feedback-launcher__label {
  visibility: visible;
}

.feedback-dock.is-dragging .feedback-launcher { cursor: grabbing; }

@media (max-width: 768px) {
  .feedback-dock {
    --feedback-size: 44px;
    --feedback-bottom: 16px;
    --feedback-anchor-top: calc(100% - var(--feedback-size));
    right: 16px;
  }

  .feedback-launcher { cursor: pointer; touch-action: auto; }
}

@media print {
  .feedback-launcher { display: none; }
}
</style>
