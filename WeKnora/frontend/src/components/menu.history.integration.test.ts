import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { compile, parse } from '@vue/compiler-dom'
import * as Vue from 'vue'

const source = readFileSync(process.env.MENU_HISTORY_TEST_SOURCE || new URL('./menu.vue', import.meta.url), 'utf8')
const template = source.slice(source.indexOf('<template>') + '<template>'.length, source.indexOf('</template>\n\n<script'))
const ast = parse(template)
function historyNode(nodes: any[]): any {
  for (const node of nodes) {
    if (node.tag === 'section' && node.props?.some((prop: any) => prop.name === 'class' && prop.value?.content === 'sidebar-recents')) return node
    const child = historyNode(node.children || [])
    if (child) return child
  }
}
const history = historyNode(ast.children)
assert.ok(history, 'recent sessions render in their own section below the primary navigation')
const render = new Function('Vue', compile(history.loc.source, { mode: 'function', prefixIdentifiers: true }).code)(Vue)

function fixture(booting: boolean) {
  const rows: any[] = [], skeletons: any[] = []
  const renderer = Vue.createRenderer<any, any>({
    createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
    insert() {}, remove() {}, setElementText() {}, setText() {}, patchProp() {},
    parentNode: () => null, nextSibling: () => null,
  })
  const items = [{ id: 'unread-session', path: '/chat/unread' }, { id: 'read-session', path: '/chat/read' }]
  const context = {
    uiStore: { sidebarCollapsed: false }, handleScroll() {}, scrollContainer: Vue.ref(null),
    sessionLookupFailedId: '', showSessionSourceFilter: false, batchMode: false,
    sessionListBooting: booting, hasAnySession: !booting,
    activeBucket: { loading: false, loaded: !booting },
    filteredGroupedSessions: booting ? [] : [{ key: 'today', label: '', items }],
    currentSecondpath: '', batchSelectedIds: [], revealedSessionId: '',
    sessionActivityEntries: {}, sessionUnread: { 'unread-session': true },
    buildSessionMenuOptions: () => [], gotopage() {}, toggleBatchSelect() {}, handleSessionMenuClick() {},
    renameSessionTitle() {}, mouseenteBotDownr() {}, mouseleaveBotDown() {},
  }
  const app = renderer.createApp({ setup: () => context, render })
  app.component('WorkspaceNavigationNode', Vue.defineComponent({
    props: { expanded: Boolean }, setup: (props, { slots }) => () => Vue.h('div', props.expanded ? slots.default?.() : []),
  }))
  app.component('t-skeleton', { setup: () => () => { skeletons.push(true); return Vue.h('i') } })
  app.component('t-loading', { render: () => Vue.h('i') })
  app.component('SessionSourceFilter', { render: () => Vue.h('div') })
  app.component('SessionSidebarRow', Vue.defineComponent({
    props: { item: { type: Object as Vue.PropType<{ id: string }>, required: true }, unread: Boolean },
    setup: (props) => () => { rows.push({ id: props.item.id, unread: props.unread }); return Vue.h('div') },
  }))
  app.mount({})
  return { rows, skeletons, dispose: () => app.unmount() }
}

test('history booting renders four placeholders without reading an unavailable session item', () => {
  const current = fixture(true)
  try {
    assert.equal(current.skeletons.length, 4)
    assert.deepEqual(current.rows, [])
  } finally { current.dispose() }
})

test('loaded recent rows retain each session unread status', () => {
  const current = fixture(false)
  try {
    assert.deepEqual(current.rows, [{ id: 'unread-session', unread: true }, { id: 'read-session', unread: false }])
    assert.deepEqual(current.skeletons, [])
  } finally { current.dispose() }
})
