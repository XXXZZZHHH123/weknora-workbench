import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { parse } from '@vue/compiler-sfc'
import { KB_WEB_TAG_RE } from '../../../utils/citationMarkdown.ts'

const source = readFileSync(new URL('./AgentStreamDisplay.vue', import.meta.url), 'utf8')
const template = parse(source).descriptor.template.ast

function findElement(node, predicate) {
  if (predicate(node)) return node
  for (const child of node.children || []) {
    const match = findElement(child, predicate)
    if (match) return match
  }
}

test('projected sources open the existing drawer without a raw answer tag', () => {
  const button = findElement(template, node => node.tag === 't-button' &&
    node.props.some(prop => prop.arg?.content === 'title' && prop.exp?.content === "$t('chat.referencesDrawerTitle')"))
  assert.ok(button)
  const condition = button.props.find(prop => prop.name === 'if').exp.content
  const click = button.props.find(prop => prop.name === 'on' && prop.arg?.content === 'click').exp.content
  const sourceRef = { id: 'checked-chunk', knowledge_title: 'Field report' }
  const session = { id: 'record', knowledge_references: [sourceRef] }
  const hasButton = (content, refs = session.knowledge_references) => Boolean(vm.runInNewContext(condition, {
    referencesDrawer: {}, session: { ...session, knowledge_references: refs }, event: { content }, KB_WEB_TAG_RE,
  }))
  assert.equal(hasButton('The observed value was 42.〔Field report〕'), true)
  assert.equal(hasButton('The observed value was 42.〔Field report〕', []), false)
  assert.equal(hasButton('The observed value was 42.<kb doc="Field report" chunk_id="checked-chunk" />'), false)

  const start = source.indexOf('const referencesDrawer = useChatReferencesDrawer()')
  const end = source.indexOf('const cleanToolOutputContent', start)
  const calls = []
  const drawer = { open: args => calls.push(args) }
  vm.runInNewContext(`${ts.transpile(source.slice(start, end))}; ${click}`, {
    props: { session }, useChatReferencesDrawer: () => drawer,
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].references[0], sourceRef)
  assert.equal(calls[0].messageId, session.id)

  const evidencePanel = findElement(template, node => node.tag === 'MessageEvidencePanel')
  assert.ok(evidencePanel.props.some(prop => prop.arg?.content === 'references' && prop.exp?.content === 'session.evidence_references'))
})
