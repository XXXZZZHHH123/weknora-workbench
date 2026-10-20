import assert from 'node:assert/strict'
import test from 'node:test'
import type { WorkbenchOverview } from '../../api/isolatedResearch'
import { buildResearchOnboardingSteps } from './researchOnboarding'

function summary(counts: Partial<WorkbenchOverview['counts']> = {}): WorkbenchOverview {
  return { workbench: { id: 'workbench', knowledge_base_id: 'library', workspace_mode: 'isolated' },
    counts: { sources: 0, documents_ready: 0, excel_pending: 0, indicators: 0, observations: 0, ...counts },
    readiness: { status: 'preparing', tasks: [] }, modules: [] }
}

test('a fresh workbench starts with its bound library and real preparation entrance', () => {
  const steps = buildResearchOnboardingSteps('Current library', summary(), 'home')
  assert.deepEqual(steps.map(step => step.key), ['scope', 'sources', 'research'])
  assert.match(steps[0]!.description!, /Current library/)
  assert.equal(steps[1]!.target, '[data-guide="research-sources"]')
})

test('pending workbooks continue review without repeating the upload step', () => {
  const steps = buildResearchOnboardingSteps('', summary({ sources: 4, excel_pending: 2, indicators: 8 }), 'home')
  assert.deepEqual(steps.map(step => step.key), ['scope', 'review', 'research'])
  assert.match(steps[1]!.description!, /2 份表格仍待核对/)
  assert.match(steps[1]!.description!, /不必重导/)
})

test('prepared workbenches reuse existing confirmed data and research', () => {
  const overview = summary({ sources: 4, indicators: 12, observations: 120 })
  overview.readiness.tasks = [{ id: 'review', status: 'ready' }, { id: 'research', status: 'ready' }]
  const snapshot = structuredClone(overview)
  const steps = buildResearchOnboardingSteps('Prepared library', overview, 'home')
  assert.deepEqual(steps.map(step => step.key), ['scope', 'ready', 'research'])
  assert.match(steps[1]!.description!, /12 个指标、120 条观测/)
  assert.equal(steps[2]!.title, '继续当前研究')
  assert.deepEqual(overview, snapshot)
})

test('documents do not claim structured indicators or require reimport', () => {
  const steps = buildResearchOnboardingSteps('', summary({ sources: 2, documents_ready: 1 }), 'home')
  assert.deepEqual(steps.map(step => step.key), ['scope', 'documents', 'research'])
  assert.match(steps[1]!.description!, /1 份文档已解析/)
  assert.doesNotMatch(steps[1]!.description!, /数据已就绪/)
})

test('opening source preparation resumes against the current drawer controls', () => {
  const empty = buildResearchOnboardingSteps('', summary(), 'sources')
  assert.equal(empty[0]!.target, '[data-guide="research-upload"]')
  const pending = buildResearchOnboardingSteps('', summary({ sources: 1, excel_pending: 1 }), 'sources')
  assert.equal(pending[0]!.key, 'review')
  assert.match(pending[0]!.target!, /research-structured/)
  assert.equal(pending.some(step => step.key === 'scope' || step.key === 'sources'), false)
})
