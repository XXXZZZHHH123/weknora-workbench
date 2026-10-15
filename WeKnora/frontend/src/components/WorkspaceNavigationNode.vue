<template>
  <div class="workspace-nav-node" :class="{ 'is-collapsed': collapsed }" :data-nav-depth="depth">
    <div class="workspace-nav-node__row" :class="{ 'is-active': active }" :style="{ '--nav-depth': depth }">
      <t-tooltip :content="title" placement="right" :disabled="!collapsed">
        <button type="button" class="workspace-nav-node__link" :title="title" :data-guide="guide"
          :aria-label="title" :aria-current="active ? 'page' : undefined" @click="emit('navigate')">
          <t-icon v-if="icon" :name="icon" :size="depth === 0 ? '18px' : '15px'" class="workspace-nav-node__icon" />
          <span v-else class="workspace-nav-node__icon-placeholder" :style="{ width: depth === 0 ? '18px' : '15px' }" aria-hidden="true" />
          <span v-if="!collapsed" class="workspace-nav-node__title">{{ title }}</span>
        </button>
      </t-tooltip>
      <slot name="actions" />
      <button v-if="expandable && !collapsed" type="button" class="workspace-nav-node__toggle"
        :aria-label="`${expanded ? '收起' : '展开'}${title}`" :aria-expanded="expanded" @click="emit('toggle')">
        <t-icon :name="expanded ? 'chevron-down' : 'chevron-right'" size="14px" />
      </button>
    </div>
    <div v-if="expandable && !collapsed" v-show="expanded" class="workspace-nav-node__children" role="group" :aria-label="title">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  title: string
  icon?: string
  depth?: number
  active?: boolean
  collapsed?: boolean
  expandable?: boolean
  expanded?: boolean
  guide?: string
}>(), { depth: 0, expanded: false })

const emit = defineEmits<{ (e: 'navigate'): void; (e: 'toggle'): void }>()
</script>

<style scoped lang="less">
.workspace-nav-node { min-width: 0; }
.workspace-nav-node__row {
  display: flex;
  min-width: 0;
  min-height: 34px;
  margin-bottom: 1px;
  border-radius: var(--app-radius-xs);
  padding-left: calc(14px + var(--nav-depth) * 14px);
  padding-right: 6px;
  color: var(--td-text-color-primary);
  transition: background-color var(--app-motion-base) ease;
  &:hover { background: var(--td-bg-color-container-hover); }
  &.is-active { color: var(--td-brand-color); background: var(--td-brand-color-light); }
}
.workspace-nav-node__link {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  padding: 6px 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.workspace-nav-node__icon { flex-shrink: 0; color: var(--td-text-color-secondary); }
.workspace-nav-node__icon-placeholder { flex-shrink: 0; }
.is-active .workspace-nav-node__icon { color: inherit; }
.workspace-nav-node__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  line-height: 20px;
  font-size: var(--app-text-base);
  font-weight: 600;
}
[data-nav-depth="1"] > .workspace-nav-node__row .workspace-nav-node__title { font-weight: 500; }
[data-nav-depth="2"] > .workspace-nav-node__row .workspace-nav-node__title { font-size: var(--app-text-sm); font-weight: 400; }
.workspace-nav-node__toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  align-self: center;
  flex: 0 0 24px;
  height: 24px;
  border: 0;
  border-radius: var(--app-radius-xs);
  background: transparent;
  color: var(--td-text-color-secondary);
  cursor: pointer;
  &:hover { background: var(--td-bg-color-container-active); }
}
.workspace-nav-node__link:focus-visible, .workspace-nav-node__toggle:focus-visible {
  outline: 2px solid var(--td-brand-color);
  outline-offset: 1px;
}
.is-collapsed > .workspace-nav-node__row { padding: 0; }
.is-collapsed > .workspace-nav-node__row .workspace-nav-node__link { justify-content: center; }
</style>
