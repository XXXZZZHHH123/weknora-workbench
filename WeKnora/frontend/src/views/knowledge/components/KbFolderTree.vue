<template>
  <aside class="kb-folder-tree" :class="{ 'is-collapsed': collapsed, 'is-navigation': navigation }">
    <div v-if="!navigation" class="kb-folder-tree__header">
      <template v-if="!collapsed">
        <span class="kb-folder-tree__title">{{ t('knowledgeBase.folderTree.title') }}</span>
        <span v-if="tree && !loading" class="kb-folder-tree__total"
          :title="t('knowledgeBase.folderTree.totalDocuments', { count: tree.total_document_count })">
          {{ t('knowledgeBase.folderTree.totalDocuments', { count: tree.total_document_count }) }}
        </span>
        <t-tooltip :content="t('knowledgeBase.folderTree.collapse')" placement="top">
          <button
            type="button"
            class="kb-folder-tree__icon-btn"
            :aria-label="t('knowledgeBase.folderTree.collapse')"
            @click="emit('update:collapsed', true)"
          >
            <t-icon name="chevron-left-double" size="15px" />
          </button>
        </t-tooltip>
      </template>
      <t-tooltip v-else :content="t('knowledgeBase.folderTree.expand')" placement="right">
        <button
          type="button"
          class="kb-folder-tree__icon-btn"
          :aria-label="t('knowledgeBase.folderTree.expand')"
          @click="emit('update:collapsed', false)"
        >
          <t-icon name="chevron-right-double" size="15px" />
        </button>
      </t-tooltip>
    </div>

    <div v-if="!collapsed" class="kb-folder-tree__body">
      <template v-if="loading && !tree">
        <div v-for="n in 5" :key="'folder-skel-' + n" class="kb-folder-tree__skeleton">
          <t-skeleton animation="gradient" :row-col="[{ width: '100%', height: '16px' }]" />
        </div>
      </template>
      <template v-else>
        <template v-for="row in rows" :key="row.kind === 'file' ? `file:${row.file.id}` : `${row.kind}:${row.path}`">
        <div
          v-if="row.kind === 'root' || row.kind === 'folder'"
          class="kb-folder-row"
          :data-folder-path="row.path"
          :class="{
            active: selectedPath === row.path,
            'is-root': row.kind === 'root',
            'is-editable': canEdit && row.kind === 'folder',
          }"
          :style="{ '--kb-folder-depth': row.depth }"
          :title="row.kind === 'root' ? t('knowledgeBase.folderTree.rootRowTip') : row.path"
          role="button"
          tabindex="0"
          @click="emit('select', row.path)"
          @keydown.enter.self="emit('select', row.path)"
          @keydown.space.prevent.self="emit('select', row.path)"
        >
          <span
            v-if="row.hasChildren"
            class="kb-folder-row__toggle"
            role="button"
            tabindex="0"
            :aria-expanded="isExpanded(row.path)"
            @keydown.enter.stop.prevent="toggle(row.path)"
            @keydown.space.stop.prevent="toggle(row.path)"
            :aria-label="t(isExpanded(row.path)
              ? 'knowledgeBase.folderTree.collapseFolder'
              : 'knowledgeBase.folderTree.expandFolder')"
            @click.stop="toggle(row.path)"
          >
            <t-icon :name="isExpanded(row.path) ? 'chevron-down' : 'chevron-right'" />
          </span>
          <span v-else class="kb-folder-row__toggle-placeholder" aria-hidden="true" />

          <t-icon
            :name="row.kind === 'root' || (row.hasChildren && isExpanded(row.path)) ? 'folder-open' : 'folder'"
            class="kb-folder-row__icon"
          />

          <span class="kb-folder-row__label">
            {{ row.kind === 'root' ? (rootLabel || t('knowledgeBase.folderTree.rootRow')) : row.name }}
          </span>
          <span class="kb-folder-row__trailing">
            <span class="kb-folder-row__count"
              :title="t('knowledgeBase.folderTree.countHint', { direct: row.documentCount, total: row.totalCount })">
              {{ row.totalCount }}
            </span>
            <KnowledgeFolderActions v-if="canEdit && row.kind === 'folder'" class="kb-folder-row__more"
              :kb-id="kbId" :folder="{ path: row.path, name: row.name, total_count: row.totalCount }" :can-edit="canEdit" :allow-delete="allowDelete"
              @renamed="emit('rename', $event)" @deleted="emit('delete', $event)" />
            <span v-else class="kb-folder-row__more-placeholder" aria-hidden="true"></span>
          </span>
        </div>
        <button v-else-if="row.kind === 'file'" type="button" class="kb-folder-row kb-folder-file"
          :class="{ active: selectedFileId === row.file.id }" :style="{ '--kb-folder-depth': row.depth }"
          :data-knowledge-id="row.file.id" :data-folder-path="row.path" :title="row.name"
          @click="emit('select-file', row.file)">
          <span class="kb-folder-row__toggle-placeholder" aria-hidden="true" />
          <t-icon name="file" class="kb-folder-row__icon" />
          <span class="kb-folder-row__label">{{ row.name }}</span>
        </button>
        <div v-else-if="row.kind === 'file-status'" class="kb-folder-files-status" :style="{ '--kb-folder-depth': row.depth }" :data-folder-path="row.path" aria-live="polite">
          <span v-if="!row.page || row.page.loading">正在加载文件…</span>
          <template v-else-if="row.page.error"><span>{{ row.page.error }}</span><button type="button" class="kb-folder-files-retry" @click="emit('load-files', row.path, true)">重试</button></template>
          <button v-else type="button" class="kb-folder-files-more" @click="emit('load-files', row.path, true)">加载更多文件</button>
        </div>
        </template>
      </template>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import KnowledgeFolderActions from './KnowledgeFolderActions.vue'
import type { KnowledgeFolderTree } from '@/api/knowledge-base/index'
import type { KnowledgeDirectoryFile, KnowledgeDirectoryFilePage } from '../../workspaces/knowledgeDirectory'
import {
  buildDirectoryRows,
  buildFolderRows,
  folderAncestorPaths,
  ROOT_FOLDER_PATH,
} from '../folderTree'

const props = withDefaults(defineProps<{
  tree: KnowledgeFolderTree | null
  kbId?: string
  /** Selected folder path; the empty string is the knowledge base top level. */
  rootLabel?: string
  selectedPath: string
  loading?: boolean
  collapsed?: boolean
  canEdit?: boolean
  navigation?: boolean
  allowDelete?: boolean
  showFiles?: boolean
  selectedFileId?: string
  filePages?: Record<string, KnowledgeDirectoryFilePage>
}>(), {
  kbId: '',
  loading: false,
  collapsed: false,
  canEdit: false,
  navigation: false,
  allowDelete: false,
  showFiles: false,
  selectedFileId: '',
  filePages: () => ({}),
})

const emit = defineEmits<{
  select: [path: string]
  'update:collapsed': [collapsed: boolean]
  rename: [payload: { from: string; to: string }]
  delete: [path: string]
  'select-file': [file: KnowledgeDirectoryFile]
  'load-files': [path: string, more?: boolean]
}>()

const { t } = useI18n()

// The root starts expanded so the uploaded structure is visible without a click.
const expanded = ref(new Set<string>([ROOT_FOLDER_PATH]))
const rows = computed(() => props.showFiles ? buildDirectoryRows(props.tree, expanded.value, props.filePages) : buildFolderRows(props.tree, expanded.value))

const isExpanded = (path: string) => expanded.value.has(path)

const toggle = (path: string) => {
  const next = new Set(expanded.value)
  if (next.has(path)) next.delete(path)
  else next.add(path)
  expanded.value = next
}

// Keep the selected folder reachable: expand the root and every folder above
// the active path, both on first load and when the selection changes from
// elsewhere (e.g. opening a folder from the document list).
watch(
  () => [props.selectedPath, props.tree] as const,
  () => {
    const next = new Set(expanded.value)
    folderAncestorPaths(props.selectedPath).forEach((path) => next.add(path))
    expanded.value = next
  },
  { immediate: true },
)

// First load also opens the top-level folders, so a two-level upload is visible
// in full without any expanding.
watch(
  () => props.tree,
  (tree) => {
    if (props.navigation && props.showFiles) return
    if (!tree?.folders?.length || expanded.value.size > 1) return
    const next = new Set(expanded.value)
    tree.folders.forEach((folder) => next.add(folder.path))
    expanded.value = next
  },
  { immediate: true },
)

watch(() => [props.tree, props.filePages, expanded.value, props.showFiles] as const, () => {
  if (!props.showFiles || !props.tree) return
  const visible = buildDirectoryRows(props.tree, expanded.value, props.filePages)
  visible.forEach(row => {
    if (row.kind === 'file-status' && !row.page) emit('load-files', row.path)
  })
}, { immediate: true })
</script>

<style scoped lang="less">
.kb-folder-tree {
  --kb-folder-indent: 14px;
  --kb-folder-base-indent: 4px;
  --kb-folder-indent-max: 4;
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  width: 220px;
  min-height: 0;
  padding-right: 12px;
  margin-right: 16px;
  border-right: 1px solid var(--td-component-stroke);
  box-sizing: border-box;

  &.is-navigation {
    --kb-folder-base-indent: calc(4px + var(--kb-folder-indent));
    width: 100%;
    padding-right: 0;
    margin-right: 0;
    border-right: 0;
  }

  &.is-collapsed {
    width: auto;
    padding-right: 8px;
    margin-right: 8px;
  }
}

.kb-folder-tree__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  height: 32px;
  padding-bottom: 16px;
  flex-shrink: 0;
}

.kb-folder-tree__title {
  font-size: var(--app-text-md);
  font-weight: 500;
  color: var(--td-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.kb-folder-tree__total {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--app-text-xs);
  font-weight: 400;
  color: var(--td-text-color-placeholder);
}

.kb-folder-tree__icon-btn {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 0;
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-secondary);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: background-color var(--app-motion-fast) ease, color var(--app-motion-fast) ease;

  &:hover {
    color: var(--td-brand-color);
    background: var(--td-bg-color-container-hover);
  }
}

.kb-folder-tree__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 8px 0 12px;
  scrollbar-width: thin;

  &::-webkit-scrollbar {
    width: 4px;
  }

  &::-webkit-scrollbar-thumb {
    border-radius: 2px;
    background: var(--td-scrollbar-color);
  }
}

.kb-folder-tree__skeleton {
  padding: 7px 8px;
}

.kb-folder-row {
  display: flex;
  align-items: center;
  gap: 4px;
  box-sizing: border-box;
  width: 100%;
  height: 32px;
  margin-bottom: 2px;
  padding: 0 4px 0 calc(var(--kb-folder-base-indent) + min(var(--kb-folder-depth, 0), var(--kb-folder-indent-max)) * var(--kb-folder-indent));
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-primary);
  font-family: var(--app-font-family);
  font-size: var(--app-text-md);
  text-align: left;
  cursor: pointer;
  user-select: none;
  transition: background var(--app-motion-fast) ease, color var(--app-motion-fast) ease;

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &:focus-visible {
    outline: none;
    outline: 2px solid var(--app-focus-border);
    outline-offset: 2px;
  }

  &.active {
    height: auto;
    min-height: 32px;
    align-items: flex-start;
    padding-top: 6px;
    padding-bottom: 6px;
    background: var(--app-selection-bg);

    .kb-folder-row__label,
    .kb-folder-row__icon {
      color: var(--td-brand-color);
    }
    .kb-folder-row__label {
      font-weight: 500;
      white-space: normal;
      overflow: visible;
      text-overflow: unset;
      overflow-wrap: anywhere;
      line-height: 18px;
    }
    .kb-folder-row__toggle,
    .kb-folder-row__toggle-placeholder,
    .kb-folder-row__icon,
    .kb-folder-row__trailing {
      margin-top: 1px;
    }
  }

  &.is-root .kb-folder-row__label {
    font-weight: 500;
  }
}

.kb-folder-row__toggle,
.kb-folder-row__toggle-placeholder {
  flex: 0 0 auto;
  width: 16px;
  height: 16px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--td-text-color-placeholder);
  border-radius: var(--app-radius-xs);
}

.kb-folder-row__toggle {
  cursor: pointer;

  &:hover {
    color: var(--td-text-color-primary);
    background: var(--td-bg-color-secondarycontainer);
  }

  .t-icon {
    font-size: var(--app-text-base);
  }
}

.kb-folder-row__icon {
  flex: 0 0 auto;
  font-size: var(--app-text-xl);
  color: var(--td-text-color-secondary);
}

.kb-folder-row__label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-folder-file { border: 0; font-size: var(--app-text-sm); }
.kb-folder-files-status {
  padding: 4px 8px 8px calc(var(--kb-folder-base-indent) + 20px + var(--kb-folder-depth, 0) * var(--kb-folder-indent));
  font-size: var(--app-text-xs);
  line-height: 1.5;
  color: var(--td-text-color-secondary);
  overflow-wrap: anywhere;
  button { padding: 2px 0; border: 0; background: transparent; color: var(--td-brand-color); font: inherit; cursor: pointer; }
  span + button { display: block; }
}

.kb-folder-row__trailing {
  position: relative;
  flex: 0 0 auto;
  min-width: 16px;
  height: 20px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  margin-left: 2px;
}

.kb-folder-row__count {
  min-width: 16px;
  text-align: right;
  font-size: var(--app-text-xs);
  color: var(--td-text-color-placeholder);
  font-variant-numeric: tabular-nums;
  line-height: 1;
}

.kb-folder-row__more {
  position: absolute;
  right: 0;
  top: 50%;
  display: inline-flex;
  opacity: 0;
  pointer-events: none;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 0;
  border-radius: var(--app-radius-xs);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-placeholder);
  align-items: center;
  justify-content: center;
  transform: translateY(-50%);
  cursor: pointer;
  transition: color var(--app-motion-fast) ease, background-color var(--app-motion-fast) ease;

  &:hover {
    color: var(--td-brand-color);
    background: var(--td-bg-color-secondarycontainer);
  }

  .t-icon {
    font-size: var(--app-text-base);
  }
}

.kb-folder-row.is-editable:hover,
.kb-folder-row.is-editable:focus-within,
.kb-folder-row.is-menu-open {
  .kb-folder-row__more {
    opacity: 1;
    pointer-events: auto;
  }

  .kb-folder-row__count {
    visibility: hidden;
  }
}

.kb-folder-row__more.is-open {
  color: var(--td-brand-color);
}

.kb-folder-row__menu {
  min-width: 140px;
}

.kb-folder-row__rename {
  flex: 1;
  min-width: 0;
  height: 22px;
  padding: 0 6px;
  border: 1px solid var(--td-brand-color);
  border-radius: var(--app-radius-xs);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  font-family: var(--app-font-family);
  font-size: var(--app-text-md);
  outline: none;
}
</style>
